# Примеры

*[English version](examples.md)*

Шесть рецептов для `owfeed.yml`, от простого к сложному. Они для авторов пакетов, у которых есть
файлы и нужен подписанный фид OpenWrt.

- Каждый рецепт — это каталог, который у вас уже есть, конфиг и четыре команды.
- owfeed пакует каталог. Сам каталог делает ваша сборка.
- Если форма фида ещё не сложилась в голове, прочитайте
  [Как это устроено](../README_ru.md#как-это-устроено). Это двадцать строк.
- Как запустить рецепт в GitHub Actions и хранить ключи в секретах, описано в
  [кукбуке](https://owfeed.org/cookbook/ru/).

## Найти быстро

| Вам нужно | Рецепт | Что ещё показывает |
|---|---|---|
| Выпустить каталог файлов | [Упаковать простой каталог](#упаковать-простой-каталог) | минимальный конфиг |
| Выпустить тему LuCI | [Упаковать тему LuCI: luci-theme-footstrap](#упаковать-тему-luci-luci-theme-footstrap) | переводы, `conffiles`, `install-if` |
| Выпустить сервис и его страницу настроек | [Упаковать сервис и его LuCI-приложение из одного репозитория](#упаковать-сервис-и-его-luci-приложение-из-одного-репозитория) | конфликты, реальные зависимости |
| Выпустить скомпилированный бинарь | [Упаковать статический Go-бинарь под несколько архитектур](#упаковать-статический-go-бинарь-под-несколько-архитектур) | несколько архитектур |
| Обслужить роутеры с apk и с opkg | [Публиковать в обе релиз-линии из одного конфига](#публиковать-в-обе-релиз-линии-из-одного-конфига) | apk и opkg |
| Уменьшить скачивание | [Обменять время сборки на меньший размер пакетов](#обменять-время-сборки-на-меньший-размер-пакетов) | `compression: zopfli` |

---

## Упаковать простой каталог

1. Разложите файлы так, как они должны установиться:

   ```
   pkg/root/
     www/luci-static/demo/style.css
     etc/config/demo
   ```

2. Напишите `owfeed.yml`:

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

3. Зафиксируйте архитектуры, затем соберите, подпишите, проиндексируйте и проверьте фид:

   ```sh
   owfeed lock --update
   owfeed build && owfeed sign && owfeed index && owfeed doctor
   ```

---

## Упаковать тему LuCI: luci-theme-footstrap

[luci-theme-footstrap](https://github.com/VizzleTF/luci-theme-footstrap) — noarch-тема LuCI: CSS,
шаблоны и переводы, ничего компилируемого. Статус-кво требует для неё 35 SDK-сборок, чтобы 35 раз
получить одни и те же байты.

`owfeed build` пакует каталог. Собирает его ваша сборка. Поэтому работа делится надвое: ваша сборка
делает rootfs, owfeed превращает его в подписанный фид.

### 1. Подготовить rootfs

Раскладка исходников — LuCI-шная, соответствие — из `luci.mk`:

| в репозитории | ставится как |
|---|---|
| `htdocs/` | `/www` |
| `ucode/` | `/usr/share/ucode/luci` |
| `luasrc/` | `/usr/lib/lua/luci` |
| `root/` | `/` |
| `i18n/<lang>/*.po` | `/usr/lib/lua/luci/i18n/<name>.<lang>.lmo` |

Скрипт ниже переводы пропускает. Их owfeed компилирует сам, на шаге 2.

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

Не кладите исходники в payload. Если `files:` направлен на дерево исходников, owfeed откажется и
назовёт найденное: `.po`, `.scss`, `node_modules`, `.DS_Store`. Без этой проверки пакет ставился бы
чисто и не содержал бы того, что дерево подразумевало.

<details>
<summary>Почему нет вызова <code>po2lmo</code></summary>

`po2lmo` — host-утилита из `luci-base`. Требовать её — значит ставить C-сборку LuCI-фида перед каждым,
кто пакует тему. У owfeed свой компилятор, побайтово совпадающий с выводом оригинала.

</details>

### 2. Написать owfeed.yml

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

Проверьте два поля, прежде чем копировать конфиг.

**`conffiles`.** Объявите каждый конфиг, который шипает пакет. Тема шипает `/etc/config/footstrap`.
Если его не объявить, sysupgrade при каждом обновлении прошивки молча заменит настройки пользователя
дефолтами пакета. `owfeed doctor` сообщает это как OWF207.

**`i18n.basename`.** Если ваш пакет никогда не шипал вариант `luci-i18n-*`, дефолт годится. По
умолчанию это имя самого `.po`-файла, как делает `luci.mk`. Здесь это было бы `footstrap.<lang>.lmo`.
Footstrap ставит `footstrap-theme`.

<details>
<summary>Почему footstrap сменил basename</summary>

Загрузчик LuCI ищет по маске `*.<lang>.lmo`, так что найдётся любое имя. Но раньше эта тема шипала
переводы отдельными пакетами `luci-i18n-footstrap-<lang>`. Роутер, обновляющийся с того релиза, всё
ещё владеет `footstrap.ru.lmo`. Занять тот же путь — конфликт файлов, и apk откажет в том самом
апгрейде.

</details>

### 3. Если переводы — отдельные пакеты, добавить `install-if`

Разнести каталоги так, как это делает `luci.mk`, по пакету `luci-i18n-<app>-<lang>` на язык, —
значит оставить дыру. Ничто другое в этом файле её не закрывает. `depends` направлен от каталога к
приложению. Поэтому установка или обновление ПРИЛОЖЕНИЯ не тянет за собой ни одного каталога.

Роутер, у которого язык был, обновляется и тихо его теряет. Именно это произошло с footstrap между
0.14.3 и 0.14.4.

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

Читается как «поставь меня сам, когда тема окажется этой версии И роутер уже русский».

- apk требует минимум двух записей, одна из которых закреплена через `=` (apk-package(5)).
- `{version}` разворачивается в собираемую версию, поэтому релиз не правит пин руками.
- Пин двигается, поэтому правило срабатывает заново на каждом обновлении приложения.
- Триггер — пакет. Пакетный менеджер не умеет читать `uci luci.main.lang`. Поэтому «роутер говорит на
  этом языке» выглядит отсюда как `luci-i18n-base-<lang>` — базовый каталог LuCI, который
  переведённый роутер уже несёт.

**У opkg условной формы этого нет.**

- `Recommends:` в opkg от OpenWrt разбирается и исполняется, но безусловно. Русский поехал бы на
  каждый роутер 24.10.
- Поставить пакет из postinst тоже нельзя: opkg держит эксклюзивную блокировку на весь запуск.
- Поэтому ipk-нога разнесённого перевода требует скрипта-установщика или задокументированного имени
  пакета. apk-нога несёт поведение сама.

Эта асимметрия — свойство формата. owfeed тут ни при чём.

### 4. Собрать фид

```sh
./stage.sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

`/etc/uci-defaults/30_luci-theme-footstrap` из темы отработает при установке без дополнительной
настройки.

<details>
<summary>Почему uci-defaults работают без дополнительной настройки</summary>

owfeed оборачивает `post-install` так же, как `package-pack.mk`, поэтому `default_postinst` применяет
uci-defaults и включает init-скрипты. Голый post-install-скрипт поставил бы файлы и не сделал бы
ничего из этого.

</details>

### 5. Дать пользователям сниппет установки

```sh
owfeed install-snippet
```

Вставьте вывод в README дословно. `doctor` следит, чтобы он не разошёлся.

---

## Упаковать сервис и его LuCI-приложение из одного репозитория

Это обычная форма для всего, у чего есть страница настроек: сервис на shell-скриптах и
LuCI-приложение к нему, в одном репозитории. Оба архитектурно независимы, `Build/Compile` пустой,
тулчейн не нужен ни одному.

1. Напишите `owfeed.yml` с обоими пакетами:

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

2. Подготовьте оба пакета. Это обычное копирование плюс подстановка версии, которую делают
   Makefile'ы:

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

3. Соберите фид. Закомментированные строки — ожидаемый вывод:

   ```sh
   owfeed lock --update
   owfeed build && owfeed sign && owfeed index && owfeed doctor
   #   built dist/noarch/netwatch-1.4.0-r1.apk
   #   built dist/noarch/luci-app-netwatch-1.4.0-r1.apk
   #     note: compiled 1 translation catalogue(s): /usr/lib/lua/luci/i18n/netwatch.ru.lmo
   #   25.12: 2 package(s) across 35 architecture(s)
   #   390 checks passed
   ```

На роутере `apk add luci-app-netwatch` вытягивает всю цепочку из официальных фидов: `curl`, `jq`,
`coreutils-base64`, `bind-dig`. Пакет ставится без `--allow-untrusted`.

### Объявить `conflicts:`, который официальная сборка теряет

Два пакета, переписывающие одну и ту же конфигурацию, не могут стоять вместе. Поэтому Makefile
объявляет `CONFLICTS:=othermon luci-app-othermon`.

На 25.12 это объявление не работает. `package-pack.mk` кладёт `Conflicts:` только в ipk-control и
никогда не передаёт в `mkpkg`. Собранный apk-пакет не несёт ничего.

apk конфликты поддерживает: это зависимость с ведущим `!`. owfeed её пишет. Поставить оба пакета
тогда не выйдет:

```
ERROR: unable to select packages:
  othermon-2026.05.06-r1:
    breaks: netwatch-1.4.0-r1[!othermon]
```

### Выбрать `i18n.basename`, если приложение раньше выпускало языковые пакеты

Пакет с `LUCI_LANGUAGES:=en ru` заставляет `luci.mk` выпускать отдельные пакеты
`luci-i18n-netwatch-<lang>`. Конфиг выше складывает каталоги внутрь `luci-app-netwatch`.

Тогда роутер, поставивший языковой пакет с прошлого релиза, уже владеет
`/usr/lib/lua/luci/i18n/netwatch.ru.lmo`. Выберите одно:

- Продолжайте выпускать языковые пакеты.
- Возьмите basename, который не столкнётся, как сделала `luci-theme-footstrap`.

owfeed за вас не угадает, и `doctor` тоже не видит чужой пакет.

---

## Упаковать статический Go-бинарь под несколько архитектур

Статическому Go-бинарю не нужен OpenWrt SDK, нужна только сборка под правильный таргет. Поэтому
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

- `{arch}` обязателен, как только архитектур больше одной. Пропуск шаблона — ошибка.
- Сборка кладётся в `dist/<arch>/`.
- Индексация кладёт `noarch`-пакет в каталог каждой архитектуры. Пер-архитектурный пакет попадает
  только в свой.
- Соответствие GOARCH → OpenWrt-архитектуры живёт в вашем fetch-скрипте. В owfeed его нет. Рабочий
  пример — в [owfeed/owfeed-packages](https://github.com/owfeed/owfeed-packages), живом фиде,
  собранном именно так.

<details>
<summary>Почему такие правила</summary>

- `{arch}`: две архитектуры не могут делить один payload. Если бы могли, пакет был бы `noarch`.
  Поэтому пропуск `{arch}` — ошибка, тихого промаха не будет.
- `dist/<arch>/`: apk выводит имя файла только из имени и версии. Две архитектуры одного пакета
  столкнулись бы в плоском каталоге.
- Соответствие GOARCH — свойство вашего тулчейна. К упаковке оно не относится.

</details>

---

## Публиковать в обе релиз-линии из одного конфига

25.12 — это apk, 24.10 — opkg. Пакет, который работает на обеих, едет в обе. Пакет, который не
работает, говорит об этом через `releases:`.

1. Объявите обе релиз-линии, по ключу подписи на каждый пакетный менеджер и линии каждого пакета:

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

2. Соберите фид. Закомментированные строки — ожидаемый вывод:

   ```sh
   owfeed build && owfeed sign && owfeed index && owfeed doctor
   #   built dist/noarch/luci-app-mine-1.0.0-r1.apk (25.12)
   #   built dist/noarch/luci-app-mine-next-1.0.0-r1.apk (25.12)
   #   built dist/all/luci-app-mine_1.0.0-r1_all.ipk (24.10)
   #   24.10: signed by usign key 3af054550a655062
   #   25.12: ... signed by key 05353a8e456d078a46325b13310c9b96
   ```

Получается одно дерево и два фида под одним URL. Роутер на 24.10 не увидит `luci-app-mine-next`
никогда: его нет в индексе той линии.

Проверено на настоящих роутерах через [owlab](https://github.com/owfeed/owlab), по одному на менеджер.

<details>
<summary>Почему каждый пакет называет свои релиз-линии</summary>

Явно указанные линии держат пакет вне индекса другой линии. Иначе пришлось бы публиковать всё везде и
надеяться, что зависимости разрулят.

</details>

---

## Обменять время сборки на меньший размер пакетов

Пакет скачивает каждый роутер, который его ставит или обновляет. Если пакет выходит часто и
сжимается за секунду-другую, задайте `compression: zopfli`:

```yaml
- name: luci-app-example
  build: mkpkg
  arch: noarch
  version: 1.0.0-r1
  files: ./dist/root
  compression: zopfli
```

- **Размер:** потоки пишутся zopfli вместо лучшего стандартного уровня deflate. В замерах они на 2-5%
  меньше.
- **Чтение:** opkg и apk читают пакеты как раньше.
- **Цена:** LuCI-пакет на 250 КБ собирается и подписывается примерно на секунду дольше. Бинарь на
  10 МБ — 30-40 с и около 600 МБ памяти на каждый шаг.
- **По умолчанию:** без этой строки ничего не меняется.

### Где выигрыш сохраняется

- `.ipk` сжимается так при сборке.
- `.apk` пережимает `owfeed sign`, после собственной подписи apk. Эта подпись пересжимает весь файл.
- Фид дальше по цепочке, который добавляет к вашему `.apk` свою подпись, пересжимает его, и выигрыш
  пропадает.
- Фид дальше по цепочке с `sign-packages: false` отдаёт ваши байты как есть.

Замеры — в
[apk-behaviour.md](apk-behaviour.md#zopfli-2-5-smaller-read-unchanged-by-apk-and-opkg-tens-of-times-slower).
