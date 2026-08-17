Rosfin terrorist-list download tool
===================================

Автоматическое скачивание из личного кабинета [Росфинмониторинга](https://portal.fedsfm.ru)
**перечня организаций и физических лиц, в отношении которых имеются сведения об их
причастности к экстремистской деятельности или терроризму**.

Скачиваются два формата актуального перечня:

| Формат          | Endpoint портала                          | Что приходит       |
|-----------------|-------------------------------------------|--------------------|
| XML (схема 2.1) | `/SkedDownload/GetActiveSked?type=xml21`  | zip-архив с `.xml` |
| Word            | `/SkedDownload/GetActiveSked?type=doc`    | zip-архив с `.doc` |

По умолчанию утилита работает как демон: скачивает сразу при старте и далее **каждые 12 часов**.

> Раньше скрипт качал формат `dbf` — портал этот тип больше не предлагает,
> поэтому клиент переведён на `xml21` и `doc`.

Быстрый старт (Docker)
----------------------

```bash
git clone https://github.com/r-pletnev/rosfin-terrorists
cd rosfin-terrorists

cp .env.example .env
# впишите ROSFIN_LOGIN и ROSFIN_PASS от личного кабинета

docker compose up -d --build
docker compose logs -f
```

Файлы появятся в `./downloads`. Остановить: `docker compose down`.

Разовый прогон в контейнере, без демона:

```bash
docker compose run --rm rosfin-terrorists -once
```

Что получается на диске
-----------------------

Каждый запуск кладёт файлы в отдельную папку с датой и временем в имени:

```
downloads/
├── 2026-08-17_09-00/
│   ├── perechen_xml.zip     # архив как отдал портал
│   ├── perechen_doc.zip
│   ├── <имя из архива>.xml  # распакованный XML
│   ├── <имя из архива>.doc  # распакованный Word
│   └── manifest.json        # время, размеры, sha256 каждого архива
├── 2026-08-17_21-00/
│   └── ...
└── latest -> 2026-08-17_21-00
```

`manifest.json` содержит sha256 — по нему легко понять, менялся ли перечень между запусками.

Настройки
---------

Всё задаётся через переменные окружения (удобно для Docker) или одноимённые флаги.

| Переменная                  | Флаг                  | По умолчанию  | Описание                                   |
|-----------------------------|-----------------------|---------------|--------------------------------------------|
| `ROSFIN_LOGIN`              | `-login`              | —             | Логин личного кабинета (обязательно)       |
| `ROSFIN_PASS`               | `-password`           | —             | Пароль личного кабинета (обязательно)      |
| `ROSFIN_OUTPUT_DIR`         | `-output`             | `./downloads` | Куда складывать файлы                      |
| `ROSFIN_INTERVAL`           | `-interval`           | `12h`         | Интервал между скачиваниями                |
| `ROSFIN_FORMATS`            | `-formats`            | `xml,doc`     | Какие форматы качать                       |
| `ROSFIN_ONCE`               | `-once`               | `false`       | Скачать один раз и выйти                   |
| `ROSFIN_TIMEOUT`            | `-timeout`            | `3m`          | Таймаут HTTP-запроса                       |
| `ROSFIN_ATTEMPTS`           | `-attempts`           | `3`           | Попыток при ошибке (пауза 5с, 10с, 20с…)   |
| `ROSFIN_EXTRACT`            | `-extract`            | `true`        | Распаковывать архивы рядом                 |
| `ROSFIN_KEEP_RUNS`          | `-keep`               | `0`           | Сколько последних папок хранить, `0` — все |
| `ROSFIN_LATEST_LINK`        | `-latest-link`        | `true`        | Обновлять симлинк `downloads/latest`       |
| `ROSFIN_MARK_NOTIFICATIONS` | `-mark-notifications` | `true`        | Отмечать уведомления кабинета прочитанными |

Запуск без Docker
-----------------

Нужен Go 1.22+:

```bash
cd go_src
make build                  # сборка в go_src/build/
make once                   # разовое скачивание
make run                    # демон каждые 12 часов
make build_for_win          # сборка exe под Windows
make test                   # тесты
```

Логин и пароль можно передать флагами или положить в `.env` в корне репозитория —
Makefile его подхватывает.

Запуск на Synology NAS
----------------------

### 1. Куда положить папку для выгрузок

В **File Station** создайте папку, например внутри общей папки `docker`:

```
/volume1/docker/rosfin/            — папка проекта (сюда лягут исходники)
/volume1/docker/rosfin/downloads/  — сюда контейнер будет складывать XML и Word
```

Папка для файлов задаётся **левой частью** строки в `volumes:`. Правую (`/data`)
менять не нужно — это путь внутри контейнера:

```yaml
volumes:
  - /volume1/docker/rosfin/downloads:/data
```

Хотите другое место — меняйте только левую часть, например
`/volume1/perechni:/data`.

### 2. Куда вписать логин и пароль

В файл `.env` в папке проекта (рядом с `docker-compose.yml`):

```
ROSFIN_LOGIN=ваш_логин
ROSFIN_PASS=ваш_пароль
```

Это те же логин и пароль, которыми вы входите на portal.fedsfm.ru.
Контейнер сам логинится ими при каждом запуске. Файл `.env` в `.gitignore` —
в репозиторий он не попадёт.

Альтернатива через интерфейс: в **Container Manager → Проект → Действие →
Редактировать**, вписать значения прямо в блок `environment:`. Менее удобно —
пароль окажется в открытом виде в конфиге проекта.

### 3. Установка (вариант через SSH, самый предсказуемый)

Включите SSH: **Панель управления → Терминал и SNMP → Включить службу SSH**.

```bash
ssh ваш_логин@адрес-nas

sudo -i
mkdir -p /volume1/docker/rosfin
cd /volume1/docker/rosfin

# исходники: клонируем ваш форк
git clone https://github.com/ВАШ_ЛОГИН/rosfin-terrorists.git .

# папка для выгрузок
mkdir -p downloads

# конфиг Synology вместо обычного
cp docker-compose.synology.yml docker-compose.yml

# логин и пароль
cp .env.example .env
vi .env          # впишите ROSFIN_LOGIN и ROSFIN_PASS

# узнайте свой uid:gid и подставьте в строку user: в docker-compose.yml
id ваш_логин
chown -R 1026:100 downloads     # свои значения из вывода id

docker compose up -d --build
docker compose logs -f
```

На DSM 7 команда — `docker compose`, на DSM 6 — `docker-compose`.
Первая сборка занимает 2–5 минут (скачивается образ Go).

Через минуту в `/volume1/docker/rosfin/downloads` появится первая папка с датой.

### 4. Установка через Container Manager, без SSH

1. **File Station** → создайте `/volume1/docker/rosfin` и внутри `downloads`.
2. Скачайте архив репозитория (Code → Download ZIP), распакуйте содержимое
   в `/volume1/docker/rosfin` (в папке должны лежать `Dockerfile`, `go_src`,
   `docker-compose.synology.yml`).
3. Переименуйте `docker-compose.synology.yml` в `docker-compose.yml`,
   `.env.example` — в `.env`, откройте `.env` в текстовом редакторе DSM
   и впишите логин с паролем.
4. **Container Manager → Проект → Создать**:
   * Название: `rosfin-terrorists`
   * Путь: `/volume1/docker/rosfin`
   * Источник: «Использовать существующий docker-compose.yml»
5. Нажмите **Далее → Готово**. DSM соберёт образ и запустит контейнер.
6. Вкладка **Журнал** покажет строки вида `сохранено: perechen_xml.zip`.

Если в логе `permission denied` при записи в `/data` — не совпал `user:`.
Проще всего исправить через **Панель управления → Планировщик заданий →
Создать → Запускаемый скрипт**, от пользователя root, разово:
`chown -R 1026:100 /volume1/docker/rosfin/downloads`.

### 5. Проверка и обслуживание

```bash
docker compose logs --tail 50        # что происходит
docker compose restart               # перечитать .env после смены пароля
docker compose run --rm rosfin-terrorists -once   # разовое скачивание сейчас
docker compose down                  # остановить
```

Расписание живёт внутри контейнера — планировщик заданий DSM настраивать не нужно.
Контейнер качает перечень сразу при старте и далее каждые 12 часов;
`restart: unless-stopped` поднимет его после перезагрузки NAS.

Заметки по эксплуатации
-----------------------

* **Права на папку.** Контейнер работает под непривилегированным `uid 10001`.
  Если `./downloads` на хосте принадлежит другому пользователю, раскомментируйте
  строку `user:` в `docker-compose.yml` и подставьте свои `id -u`/`id -g`.
* **Ошибки не роняют демон.** Неудачный запуск логируется, процесс ждёт следующего
  тика; `restart: unless-stopped` поднимет контейнер после перезагрузки хоста.
* **Проверка ответа.** Если сессия протухла и портал вместо файла отдаёт HTML
  страницы логина, утилита распознаёт это и повторяет попытку, а не сохраняет мусор.
  Пустой ответ тоже считается ошибкой.
* **Секреты.** `.env` в `.gitignore`. Не коммитьте логин и пароль.

Python-версия
-------------

`py_src/` оставлена как есть, для истории — она всё ещё качает `dbf`.
Актуальная и поддерживаемая версия — Go в `go_src/`.
