# Примеры

*[English version](examples.md)*

Пять разобранных примеров, от простого к сложному. Каждый — это каталог, который у вас уже есть,
конфиг и четыре команды. Если форма фида ещё не сложилась в голове,
[Как это устроено](../README_ru.md#как-это-устроено) — двадцать строк.

- [Самое простое, что работает](#самое-простое-что-работает)
- [Тема LuCI: luci-theme-footstrap](#тема-luci-luci-theme-footstrap) — переводы
- [Сервис и его LuCI-приложение из одного репозитория](#сервис-и-его-luci-приложение-из-одного-репозитория) — конфликты,
  реальные зависимости
- [Скомпилированный бинарь: статический Go-демон](#скомпилированный-бинарь-статический-go-демон) — несколько архитектур
- [Обе релиз-линии из одного конфига](#обе-релиз-линии-из-одного-конфига) — apk и opkg

---

## Самое простое, что работает

Каталог файлов, разложенный так, как он должен установиться:

```
pkg/root/
  www/luci-static/demo/style.css
  etc/config/demo
```

```yaml
version: 1
feed:
  name: demofeed
  url: https://feed.example.org
publish:
  - target: github-pages
packages:
  - name: luci-app-demo
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./pkg/root
    depends: [luci-base]
    conffiles: ["/etc/config/demo"]
```

```sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

---

## Тема LuCI: luci-theme-footstrap

[luci-theme-footstrap](https://github.com/VizzleTF/luci-theme-footstrap) — noarch-тема LuCI: CSS,
шаблоны и переводы, ничего компилируемого. Ровно тот случай, где статус-кво требует 35 SDK-сборок,
чтобы 35 раз получить одни и те же байты.

`owfeed build` пакует каталог, а не собирает его. Поэтому работа делится надвое: ваша сборка делает
rootfs, owfeed превращает его в подписанный фид.

### 1. Подготовить rootfs

Раскладка исходников — LuCI-шная, соответствие — из `luci.mk`:

| в репозитории | ставится как |
|---|---|
| `htdocs/` | `/www` |
| `ucode/` | `/usr/share/ucode/luci` |
| `luasrc/` | `/usr/lib/lua/luci` |
| `root/` | `/` |
| `i18n/<lang>/*.po` | `/usr/lib/lua/luci/i18n/<name>.<lang>.lmo` |

Переводов в таблице нет, потому что их owfeed компилирует сам — см. шаг 2.

```sh
#!/bin/sh
# stage.sh — сделать dist/root, каталог, который упакует owfeed.
set -e
SRC=luci-theme-footstrap
DIST=dist/root
rm -rf dist && mkdir -p "$DIST"

./"$SRC"/build-css.sh                       # минифицированный CSS в htdocs/

mkdir -p "$DIST/www" "$DIST/usr/share/ucode/luci"
cp -a "$SRC"/htdocs/. "$DIST/www/"
cp -a "$SRC"/ucode/.  "$DIST/usr/share/ucode/luci/"
cp -a "$SRC"/root/.   "$DIST/"

git describe --tags --abbrev=0 | sed 's/^v//;s/$/-r1/' > dist/VERSION
```

Вызова `po2lmo` нет, и он не нужен: это host-утилита из `luci-base`, требовать её — значит ставить
C-сборку LuCI-фида перед каждым, кто пакует тему. У owfeed свой компилятор, побайтово совпадающий с
выводом оригинала.

Исходники в payload не попадают никогда. Направьте `files:` на дерево исходников — owfeed откажется
и назовёт файл: `.po`, `.scss`, `node_modules`, `.DS_Store`. Лучше так, чем пакет, который ставится
чисто и не содержит того, что дерево подразумевало.

### 2. owfeed.yml

```yaml
version: 1

feed:
  name: footstrap
  url: https://feed.footstrap.dev
  title: Footstrap
  maintainer: "VizzleTF <vizzletf47@gmail.com>"
  license: Apache-2.0
  homepage: https://github.com/VizzleTF/luci-theme-footstrap

publish:
  - target: github-pages

packages:
  - name: luci-theme-footstrap
    build: mkpkg
    arch: noarch                          # никогда "all" — apk такое отвергает
    version-from: file:./dist/VERSION
    files: ./dist/root
    description: "A modern, fast LuCI theme."
    depends: [luci-base]
    conffiles: ["/etc/config/footstrap"]
    i18n:
      from: ./luci-theme-footstrap/i18n     # каталог с <lang>/*.po
      basename: footstrap-theme             # -> footstrap-theme.<lang>.lmo
```

Два поля стоит прочитать дважды.

**`conffiles`.** Тема шипает `/etc/config/footstrap`; не объявить его — значит, что sysupgrade при
каждом обновлении прошивки молча заменит настройки пользователя дефолтами пакета. `owfeed doctor`
сообщает это как OWF207.

**`i18n.basename`.** По умолчанию — имя самого `.po`-файла, как делает `luci.mk`; здесь это было бы
`footstrap.<lang>.lmo`. Footstrap ставит `footstrap-theme`, и причину стоит знать до того, как
выберете своё. Загрузчик LuCI ищет по маске `*.<lang>.lmo`, так что найдётся любое имя. Но раньше
эта тема шипала переводы отдельными пакетами `luci-i18n-footstrap-<lang>`, и роутер, обновляющийся
с того релиза, всё ещё владеет `footstrap.ru.lmo`. Занять тот же путь — конфликт файлов, и apk
откажет в том самом апгрейде. Если ваш пакет никогда не шипал вариант `luci-i18n-*`, дефолт годится.

**`install-if`, когда переводы — отдельные пакеты.** Разнести каталоги так, как это делает
`luci.mk` — по пакету `luci-i18n-<app>-<lang>` на язык, — оставляет дыру, которую ничто другое в
этом файле не закрывает: `depends` направлен от каталога к приложению, поэтому установка или
обновление ПРИЛОЖЕНИЯ не тянет за собой ни одного каталога. Роутер, у которого язык был,
обновляется и тихо его теряет — именно это произошло с footstrap между 0.14.3 и 0.14.4.

У apk есть поле ровно для этого, и owfeed его пробрасывает:

```yaml
  - name: luci-i18n-footstrap-ru
    build: mkpkg
    arch: noarch
    version-from: file:./dist/VERSION
    files: ./dist/i18n-ru
    depends: [luci-theme-footstrap]
    install-if: [ "luci-theme-footstrap={version}", "luci-i18n-base-ru" ]
```

Читается как «поставь меня сам, когда тема окажется этой версии И роутер уже русский». apk требует
минимум двух записей, одна из которых закреплена через `=` (apk-package(5)); `{version}`
разворачивается в собираемую версию, поэтому релиз не правит пин руками, а раз пин двигается,
правило срабатывает заново на каждом обновлении приложения.

Триггер — пакет, а не настройка: пакетный менеджер не умеет читать `uci luci.main.lang`, поэтому
«роутер говорит на этом языке» выглядит отсюда как `luci-i18n-base-<lang>` — базовый каталог LuCI,
который переведённый роутер уже несёт.

**У opkg условной формы этого нет.** `Recommends:` в opkg от OpenWrt разбирается и исполняется, но
безусловно: русский поехал бы на каждый роутер 24.10. Поставить пакет из postinst тоже нельзя —
opkg держит эксклюзивную блокировку на весь запуск. Поэтому ipk-нога разнесённого перевода требует
скрипта-установщика или задокументированного имени пакета, а apk-нога несёт поведение сама. Эта
асимметрия — свойство формата, а не owfeed.

### 3. Собрать фид

```sh
./stage.sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

`/etc/uci-defaults/30_luci-theme-footstrap` из темы отработает при установке без дополнительной
настройки: owfeed оборачивает `post-install` так же, как `package-pack.mk`, поэтому
`default_postinst` применяет uci-defaults и включает init-скрипты. Голый post-install-скрипт поставил
бы файлы и не сделал бы ничего из этого.

### 4. Что выполняют пользователи

```sh
owfeed install-snippet
```

Вывод вставляется в README дословно. `doctor` следит, чтобы он не разошёлся.

---

## Сервис и его LuCI-приложение из одного репозитория

Обычная форма для всего, у чего есть страница настроек: сервис на shell-скриптах и LuCI-приложение
к нему, в одном репозитории. Оба архитектурно независимы, `Build/Compile` пустой, тулчейн не нужен
ни одному.

```yaml
version: 1

feed:
  name: netwatch
  url: https://feed.example.org
  title: netwatch
  maintainer: "Вы <you@example.org>"
  license: GPL-2.0-or-later
  homepage: https://example.org/netwatch

publish:
  - target: github-pages

packages:
  - name: netwatch
    build: mkpkg
    arch: noarch
    version-from: file:./VERSION
    files: ./staging/netwatch
    description: "Link monitoring daemon"
    depends: [curl, jq, coreutils-base64, bind-dig]
    conflicts: [othermon, luci-app-othermon]
    conffiles: ["/etc/config/netwatch"]

  - name: luci-app-netwatch
    build: mkpkg
    arch: noarch
    version-from: file:./VERSION
    files: ./staging/luci-app-netwatch
    description: "LuCI netwatch app"
    depends: [luci-base, netwatch]
    i18n:
      from: ./luci-app-netwatch/po
      basename: netwatch
```

Подготовка — обычное копирование плюс подстановка версии, которую делают Makefile'ы:

```sh
#!/bin/sh
set -e
VER="1.4.0"; echo "$VER-r1" > VERSION

# luci-app-netwatch: htdocs -> /www, root -> /
mkdir -p staging/luci-app-netwatch/www
cp -a luci-app-netwatch/htdocs/. staging/luci-app-netwatch/www/
cp -a luci-app-netwatch/root/.   staging/luci-app-netwatch/

# netwatch: files/ ложится 1:1, кроме usr/lib/* -> /usr/lib/netwatch/
mkdir -p staging/netwatch/usr/lib/netwatch
cp -a netwatch/files/etc netwatch/files/usr/bin staging/netwatch/
cp -a netwatch/files/usr/lib/.                  staging/netwatch/usr/lib/netwatch/

grep -rl __COMPILED_VERSION_VARIABLE__ staging | xargs sed -i "s/__COMPILED_VERSION_VARIABLE__/$VER/g"
```

```sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
#   built dist/noarch/netwatch-1.4.0-r1.apk
#   built dist/noarch/luci-app-netwatch-1.4.0-r1.apk
#     note: compiled 1 translation catalogue(s): /usr/lib/lua/luci/i18n/netwatch.ru.lmo
#   25.12: 2 package(s) across 35 architecture(s)
#   390 checks passed
```

На роутере `apk add luci-app-netwatch` вытягивает всю цепочку — `curl`, `jq`,
`coreutils-base64`, `bind-dig` — из официальных фидов и ставится **без** `--allow-untrusted`.

### `conflicts:` делает то, чего не умеет официальная сборка

Два пакета, переписывающие одну и ту же конфигурацию, не могут стоять вместе, поэтому Makefile
объявляет `CONFLICTS:=othermon luci-app-othermon`. На 25.12 это объявление не работает:
`package-pack.mk` кладёт `Conflicts:` только в ipk-control и никогда не передаёт в `mkpkg`, так что
собранный apk-пакет не несёт ничего.

apk конфликты поддерживает — это зависимость с ведущим `!` — и owfeed их пишет:

```
ERROR: unable to select packages:
  othermon-2026.05.06-r1:
    breaks: netwatch-1.4.0-r1[!othermon]
```

### Про `i18n.basename` здесь

Пакет с `LUCI_LANGUAGES:=en ru` заставляет `luci.mk` выпускать отдельные пакеты
`luci-i18n-netwatch-<lang>`. Сложить каталоги внутрь `luci-app-netwatch`, как делает конфиг выше,
означает, что роутер, поставивший языковой пакет с прошлого релиза, уже владеет
`/usr/lib/lua/luci/i18n/netwatch.ru.lmo`. Либо продолжайте выпускать языковые пакеты, либо возьмите
basename, который не столкнётся, как сделала `luci-theme-footstrap`. owfeed за вас не угадает —
`doctor` тоже не видит чужой пакет.

---

## Скомпилированный бинарь: статический Go-демон

Статическому Go-бинарю не нужен OpenWrt SDK — нужна сборка под правильный таргет, — поэтому
SDK-less путь не ограничен `noarch`. Один апстримный артефакт обычно покрывает несколько
OpenWrt-архитектур с общим GOARCH: одна сборка `arm64` закрывает все четыре `aarch64_*`.

```yaml
- name: example-daemon
  build: mkpkg
  arch:
    - x86_64                 # GOARCH=amd64
    - aarch64_cortex-a53     # GOARCH=arm64, все четыре
    - aarch64_cortex-a72
    - aarch64_cortex-a76
    - aarch64_generic
    - mipsel_24kc            # GOARCH=mipsle, GOMIPS=softfloat
    - mipsel_74kc
  version-from: file:./staging/example-daemon.version
  files: ./staging/example-daemon/{arch}
  description: "Одна строка. LuCI обрезает после 512 байт."
```

`{arch}` обязателен, как только архитектур больше одной. Две архитектуры не могут делить один
payload — если бы могли, пакет был бы `noarch`, — поэтому пропуск шаблона это ошибка, а не тихий
промах.

Сборка кладётся в `dist/<arch>/`, потому что apk выводит имя файла только из имени и версии: две
архитектуры одного пакета столкнулись бы в плоском каталоге. Индексация потом кладёт `noarch`-пакет
в каталог каждой архитектуры, а пер-архитектурный — только в свой.

Соответствие GOARCH → OpenWrt-архитектуры живёт в вашем fetch-скрипте, а не в owfeed: это свойство
вашего тулчейна, а не упаковки. Рабочий пример — в
[owfeed/owfeed-packages](https://github.com/owfeed/owfeed-packages), живом фиде, собранном
именно так.

---

## Обе релиз-линии из одного конфига

25.12 — это apk, 24.10 — opkg. Пакет, который работает на обеих, едет в обе; тот, который нет, — говорит об этом.

```yaml
releases:
  - line: "25.12"
    default: true
    format: apk
  - line: "24.10"
    format: ipk

signing:
  key: env:OWFEED_SIGN_KEY        # EC, для apk
  usign-key: env:OWFEED_USIGN_KEY # usign, для opkg — каждый менеджер проверяет только свою схему

packages:
  - name: luci-app-mine           # нет `releases:` — публикуется в обе
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./dist/root
    url: https://github.com/you/mine

  - name: luci-app-mine-next
    releases: ["25.12"]           # только 25.12
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./next/root
    url: https://github.com/you/mine
```

```sh
owfeed build && owfeed sign && owfeed index && owfeed doctor
#   built dist/noarch/luci-app-mine-1.0.0-r1.apk (25.12)
#   built dist/all/luci-app-mine_1.0.0-r1_all.ipk (24.10)
#   24.10: signed by usign key 3af054550a655062
```

Одно дерево, два фида под одним URL. Роутер на 24.10 не увидит `luci-app-mine-next` никогда: его нет
в индексе той линии. Ради этого линии и указываются явно, а не «публикуем всё везде и надеемся, что
зависимости разрулят».

Проверено на настоящих роутерах через [owlab](https://github.com/owfeed/owlab), по одному на менеджер.
