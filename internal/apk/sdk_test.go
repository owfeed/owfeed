package apk

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"owfeed.org/owfeed/internal/netx"
	"owfeed.org/owfeed/internal/usign"
)

const testRelease = "25.12.5"

// toServer sends every request to one local server, whatever host it names, so that
// the real downloads.openwrt.org and raw.githubusercontent.com URLs reach the test.
type toServer struct{ base *url.URL }

func (r toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = r.base.Scheme, r.base.Host, r.base.Host
	return http.DefaultTransport.RoundTrip(req)
}

// fakeUpstream stands in for OpenWrt: an SDK tarball holding sdkMembers, a sha256sums
// listing it, signed by a key the test pins for its own duration.
type fakeUpstream struct {
	t       *testing.T
	members map[string][]byte
	tarball []byte
	name    string
	keyID   string

	mu       sync.Mutex
	requests []string // path and Range of each tarball request
	hits     int      // every request, of any file
	// stall, when set, decides per tarball request (1-based) where the body stops
	// and the connection then stays open with nothing more sent.
	stall func(n int) int
	end   chan struct{}
}

func newUpstream(t *testing.T) (*fakeUpstream, *http.Client) {
	t.Helper()
	old := netx.DefaultDelay
	netx.DefaultDelay = time.Millisecond
	t.Cleanup(func() { netx.DefaultDelay = old })

	u := &fakeUpstream{t: t, members: map[string][]byte{}, end: make(chan struct{})}
	top := "openwrt-sdk-" + testRelease + "-x86-64_gcc-14.3.0_musl.Linux-x86_64"
	u.name = top + ".tar.zst"

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	add := func(name string, body []byte, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	// Incompressible filler ahead of the members, so that a download cut in the
	// middle is cut before anything has been extracted.
	filler := make([]byte, 256<<10)
	if _, err := rand.Read(filler); err != nil {
		t.Fatal(err)
	}
	add("staging_dir/host/share/filler", filler, 0o644)
	for _, m := range sdkMembers {
		body := []byte("contents of " + m)
		u.members[m] = body
		add(m, body, 0o755)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var zBuf bytes.Buffer
	zw, err := zstd.NewWriter(&zBuf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	u.tarball = zBuf.Bytes()

	key, err := usign.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := key.MarshalPublic("owfeed test key")
	id := key.ID.String()
	u.keyID = id
	trustedKeys[id] = sha256hex(pub)
	t.Cleanup(func() { delete(trustedKeys, id) })

	sums := []byte(fmt.Sprintf("%s *openwrt-imagebuilder-%s-x86-64.Linux-x86_64.tar.zst\n%s *%s\n",
		sha256hex([]byte("other")), testRelease, sha256hex(u.tarball), u.name))
	sig := key.Sign(sums, "owfeed test signature")
	base := "/releases/" + testRelease + "/targets/" + sdkTarget + "/"

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits++
		u.mu.Unlock()
		switch r.URL.Path {
		case base + "sha256sums":
			_, _ = w.Write(sums)
		case base + "sha256sums.sig":
			_, _ = w.Write(sig)
		case "/openwrt/keyring/" + keyringCommit + "/usign/" + id:
			_, _ = w.Write(pub)
		case base + u.name:
			u.mu.Lock()
			u.requests = append(u.requests, r.Header.Get("Range"))
			n := len(u.requests)
			u.mu.Unlock()
			if u.stall != nil {
				if cut := u.stall(n); cut > 0 {
					from := 0
					if rg := r.Header.Get("Range"); rg != "" {
						fmt.Sscanf(rg, "bytes=%d-", &from)
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(u.tarball)-1, len(u.tarball)))
						w.WriteHeader(http.StatusPartialContent)
					}
					_, _ = w.Write(u.tarball[from:max(from, cut)])
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-u.end:
					}
					return
				}
			}
			http.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(u.tarball))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	// After the server's own cleanup, so it runs first and lets stalled handlers go.
	t.Cleanup(func() { close(u.end) })

	target, _ := url.Parse(s.URL)
	hc := &http.Client{Transport: &netx.Transport{Base: toServer{target}, Delay: time.Millisecond, Idle: 250 * time.Millisecond}}
	return u, hc
}

func (u *fakeUpstream) hitCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// acquire runs Acquire, failing the test if it has not returned within a few seconds
// rather than leaving it to hang until go test's own timeout.
func acquire(t *testing.T, hc *http.Client, cache string) (string, error) {
	t.Helper()
	var dir string
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		dir, err = Acquire(context.Background(), hc, cache, testRelease)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Acquire still running after 10s")
	}
	return dir, err
}

func (u *fakeUpstream) assertExtracted(t *testing.T, dir string) {
	t.Helper()
	for m, want := range u.members {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(m)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %q, %v; want %q", m, got, err, want)
		}
	}
	if err := checkCache(dir); err != nil {
		t.Fatalf("a fresh extraction fails its own check: %v", err)
	}
}

func TestAcquireUsesItsOwnRecordWithoutTheNetwork(t *testing.T) {
	u, hc := newUpstream(t)
	cache := t.TempDir()

	dir, err := acquire(t, hc, cache)
	if err != nil {
		t.Fatal(err)
	}
	u.assertExtracted(t, dir)
	for _, f := range []string{sumsFile, sigFile, keyFile, membersFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("no %s beside the extraction: %v", f, err)
		}
	}

	before := u.hitCount()
	again, err := acquire(t, hc, cache)
	if err != nil || again != dir {
		t.Fatalf("second Acquire: %q, %v; want %q", again, err, dir)
	}
	if n := u.hitCount() - before; n != 0 {
		t.Fatalf("a cache that passes its check made %d request(s), want none", n)
	}
}

// Before checkCache, every one of these was trusted: the files are all there and none
// of them is empty.
func TestAcquireReplacesACacheThatFailsItsCheck(t *testing.T) {
	cases := map[string]struct {
		damage func(t *testing.T, dir string)
		says   string
	}{
		"a member changed": {
			damage: func(t *testing.T, dir string) {
				appendTo(t, filepath.Join(dir, filepath.FromSlash(binPath)), "!")
			},
			says: ".apk.bin hashes to",
		},
		"a member truncated": {
			damage: func(t *testing.T, dir string) {
				if err := os.Truncate(filepath.Join(dir, filepath.FromSlash(loaderPath)), 3); err != nil {
					t.Fatal(err)
				}
			},
			says: "ld-linux-x86-64.so.2 hashes to",
		},
		"the record rewritten without its signature": {
			damage: func(t *testing.T, dir string) {
				appendTo(t, filepath.Join(dir, sumsFile), "0000 *extra\n")
			},
			says: "sha256sums from " + "DIR",
		},
		"no record, as owfeed 0.5.5 left it": {
			damage: func(t *testing.T, dir string) {
				for _, f := range []string{sumsFile, sigFile, keyFile, membersFile} {
					if err := os.Remove(filepath.Join(dir, f)); err != nil {
						t.Fatal(err)
					}
				}
			},
			says: "no such file",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			u, hc := newUpstream(t)
			cache := t.TempDir()
			dir, err := acquire(t, hc, cache)
			if err != nil {
				t.Fatal(err)
			}

			c.damage(t, dir)
			err = checkCache(dir)
			if says := strings.ReplaceAll(c.says, "DIR", dir); err == nil || !strings.Contains(err.Error(), says) {
				t.Fatalf("checkCache = %v, want an error saying %q", err, says)
			}

			before := len(u.requests)
			if _, err := acquire(t, hc, cache); err != nil {
				t.Fatal(err)
			}
			if len(u.requests) == before {
				t.Fatal("Acquire used a cache that fails its check")
			}
			u.assertExtracted(t, dir)
		})
	}
}

// A key that a later owfeed stops pinning retires every cache verified with it, rather
// than leaving extractions it vouched for in use.
func TestAcquireRetiresACacheWhoseKeyIsNoLongerPinned(t *testing.T) {
	u, hc := newUpstream(t)
	cache := t.TempDir()
	dir, err := acquire(t, hc, cache)
	if err != nil {
		t.Fatal(err)
	}
	delete(trustedKeys, u.keyID)

	if err := checkCache(dir); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("checkCache = %v, want a key that is not pinned", err)
	}
	got, err := acquire(t, hc, cache)
	if err == nil || got != "" || !strings.Contains(err.Error(), "not pinned") || netx.Transient(err) {
		t.Fatalf("Acquire = %q, %v; want a key that is not pinned, as an answer rather than an outage", got, err)
	}
}

// The old extraction goes only when a verified one is ready to take its place, so an
// outage in the middle of replacing it costs nothing that was there.
func TestAcquireKeepsTheOldCacheWhenTheReplacementFails(t *testing.T) {
	u, hc := newUpstream(t)
	cache := t.TempDir()
	dir, err := acquire(t, hc, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, membersFile)); err != nil {
		t.Fatal(err)
	}
	u.stall = func(int) int { return len(u.tarball) / 2 }

	if _, err := acquire(t, hc, cache); !netx.Transient(err) {
		t.Fatalf("Acquire = %v, want an outage", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(binPath))); err != nil {
		t.Fatalf("a failed download removed the extraction it was replacing: %v", err)
	}
}

// The release names a directory that Acquire may delete and a URL it fetches, so a
// release that is not one is refused before either.
func TestAcquireRefusesAReleaseThatIsNotOne(t *testing.T) {
	u, hc := newUpstream(t)
	cache := t.TempDir()
	keep := filepath.Join(cache, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"", "..", "../..", "25.12", "SNAPSHOT", "25.12.5/../..", "25.12.5-beta"} {
		_, err := Acquire(context.Background(), hc, filepath.Join(cache, "a", "b"), release)
		if err == nil || !strings.Contains(err.Error(), "not a point release") || netx.Transient(err) {
			t.Errorf("Acquire(%q) = %v, want a release that is not one", release, err)
		}
	}
	if n := u.hitCount(); n != 0 {
		t.Errorf("%d request(s) for releases that are not ones", n)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"25.12.5", "24.10.0-rc1"} {
		if !pointRE.MatchString(release) {
			t.Errorf("%q is a point release", release)
		}
	}
}

func TestAcquireResumesAStalledDownload(t *testing.T) {
	u, hc := newUpstream(t)
	half := len(u.tarball) / 2
	u.stall = func(n int) int {
		if n == 1 {
			return half
		}
		return 0
	}

	dir, err := acquire(t, hc, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u.assertExtracted(t, dir)
	if want := []string{"", fmt.Sprintf("bytes=%d-", half)}; fmt.Sprint(u.requests) != fmt.Sprint(want) {
		t.Fatalf("tarball requests with Range %q, want %q", u.requests, want)
	}
}

func TestAcquireGivesUpOnADownloadThatStaysStalled(t *testing.T) {
	u, hc := newUpstream(t)
	half := len(u.tarball) / 2
	u.stall = func(int) int { return half }

	cache := t.TempDir()
	_, err := acquire(t, hc, cache)
	var oe *netx.OutageError
	if !errors.As(err, &oe) || !netx.Transient(err) {
		t.Fatalf("Acquire = %v, want an outage", err)
	}
	if !strings.Contains(err.Error(), u.name) || !strings.Contains(err.Error(), fmt.Sprintf("broke off at byte %d", half)) {
		t.Fatalf("the error should name the file and where it stopped: %v", err)
	}
	if len(u.requests) != netx.DefaultAttempts {
		t.Fatalf("%d tarball requests, want %d", len(u.requests), netx.DefaultAttempts)
	}
	entries, _ := os.ReadDir(filepath.Join(cache, "sdk"))
	if len(entries) != 0 {
		t.Fatalf("a failed Acquire left %v in the cache", entries)
	}
}

func appendTo(t *testing.T, file, s string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
