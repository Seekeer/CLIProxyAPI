# Переключение сервера на образ Seekeer

Образ `ghcr.io/seekeer/cliproxyapi` содержит бэкенд и панель управления.
Поддерживаются Linux amd64 и arm64. Рабочие данные в образ не включаются.

## Публикация

Workflow [Publish fork image](https://github.com/Seekeer/CLIProxyAPI/actions/workflows/fork-image.yml)
запускается после push в `main` бэкенда, вручную через **Run workflow** и ежедневно
в 03:23 UTC. Он собирает текущий коммит бэкенда и `main` панели из
`Seekeer/Cli-Proxy-API-Management-Center`. После изменения только панели запустите
workflow вручную либо дождитесь ежедневной сборки.

Синхронизация форков с upstream этим workflow не выполняется.
Обе архитектуры собираются параллельно на нативных runners. Перед обновлением
общего тега и `latest` workflow запускает каждый контейнер с тестовой конфигурацией
и сверяет HTTP-ответ `/management.html` со встроенным файлом панели. Если сборка
или эта проверка не прошла, общий тег не публикуется, а `latest` не обновляется.
Публикация использует встроенный `GITHUB_TOKEN` с `packages: write`; отдельный
секрет для GHCR не требуется. Первая публикация может создать приватный пакет.
Для скачивания без авторизации откройте пакет в профиле Seekeer → Package settings
→ Change visibility → Public. Если пакет оставлен приватным, серверу потребуется
авторизация GHCR токеном с `read:packages` через `docker login ghcr.io`.

В summary успешного запуска есть тег с коммитами обоих компонентов и ссылка вида:

```text
ghcr.io/seekeer/cliproxyapi@sha256:...
```

Для сервера используйте эту полную ссылку с digest. `latest` обновляется после
каждой успешной публикации и удобен для тестового окружения.

## Перед переключением i3

Ниже предполагается существующий Docker Compose deployment. Выполняйте команды
из его каталога. Замените `cli-proxy-api`, если сервис называется иначе. Сохраните
его текущие порты, сети, environment, command, restart policy и все volume mounts.
Новый Compose из репозитория поверх рабочего копировать не нужно.

1. Убедитесь, что workflow завершился успешно. Скачайте новый образ до остановки
   сервиса, подставив настоящий digest из summary:

   ```bash
   export FORK_IMAGE='ghcr.io/seekeer/cliproxyapi@sha256:REPLACE_WITH_REAL_DIGEST'
   docker pull "$FORK_IMAGE"
   ```

2. Сохраните рабочий Compose, `.env` (если есть) и `config.yaml`. Найдите реальные
   источники bind mounts и named volumes через `docker inspect`. Подготовьте
   резервную копию auth-файлов, plugins и используемой базы данных/хранилища.
   Копии содержат секреты: храните их с ограниченными правами. Не публикуйте
   `docker compose config` или содержимое конфигурации в тикетах.

3. Закрепите прежний образ локальным тегом, чтобы он остался доступен для отката:

   ```bash
   container_id=$(docker compose ps -q cli-proxy-api)
   old_image_id=$(docker inspect --format '{{.Image}}' "$container_id")
   rollback_tag="local/cli-proxy-api:rollback-$(date +%Y%m%d-%H%M%S)"
   docker image tag "$old_image_id" "$rollback_tag"
   printf '%s\n' "$rollback_tag"
   ```

4. Запланируйте короткое прерывание активных запросов. Для согласованной файловой
   копии остановите сервис и затем скопируйте его данные. Не используйте
   `docker compose down -v`: это удаляет named volumes.

   ```bash
   docker compose stop cli-proxy-api
   # Make the final backup of config and persistent data now.
   ```

## Изменения конфигурации

В существующем `config.yaml` добавьте поле в блок `management` (актуальная схема
v8), сохранив management key и остальные настройки. Не создавайте второй блок:

```yaml
management:
  disable-auto-update-panel: true
```

Если сервер ещё использует старую схему с `remote-management`, этот параметр
добавляется в существующий старый блок. Не оставляйте оба варианта одновременно:
при наличии обоих приоритет имеет `management`.

Это сохраняет встроенную панель вместо автоматического скачивания официальной.
Образ задаёт `MANAGEMENT_STATIC_PATH=/opt/cli-proxy-panel`. Если в Compose уже
задан `MANAGEMENT_STATIC_PATH`, удалите переопределение или измените его на этот
путь. Не монтируйте старую панель поверх `/opt/cli-proxy-panel`.

В существующем сервисе Compose поменяйте только образ и политику скачивания:

```yaml
services:
  cli-proxy-api:
    image: ${CLI_PROXY_IMAGE:?Set CLI_PROXY_IMAGE to the published digest}
    pull_policy: missing
    # Keep existing ports, volumes, environment and other service settings.
```

В существующем `.env` задайте полный опубликованный digest:

```dotenv
CLI_PROXY_IMAGE=ghcr.io/seekeer/cliproxyapi@sha256:REPLACE_WITH_REAL_DIGEST
```

Если сервис содержит `build:`, удалите его либо всегда используйте `--no-build`,
чтобы не запустить локальную сборку вместо опубликованного образа.

## Запуск и проверка

```bash
docker compose up -d --no-deps --no-build cli-proxy-api
docker compose ps cli-proxy-api
docker compose logs --tail=100 cli-proxy-api
```

Проверьте без публикации секретов:

- По прежнему адресу открывается `/management.html`, вход с прежним management key
  работает. При необходимости обновите страницу без браузерного кеша.
- Обычный API-запрос проходит с существующим клиентским ключом.
- В редакторе клиентского ключа доступны три режима выбора источников.
- Созданный тестовый ключ с одним выбранным upstream использует только его;
  при его недоступности запрос не переходит к auth-файлам или чужим API-ключам.
- Правило сохраняется после обновления страницы и перезапуска контейнера.

Проверку маршрутизации делайте отдельным тестовым клиентским ключом. Не меняйте
ограничения рабочих клиентов до завершения проверки. Браузерная проверка нового
редактора на момент подготовки инструкции ещё не завершена.

## Откат

1. Остановите сервис.
2. Восстановите прежнюю конфигурацию и настройки Compose из резервной копии.
   Старый бэкенд может не поддерживать `access.key-routing`; возврат только образа
   без конфигурации не считается безопасным откатом ограничений доступа.
3. Укажите сохранённый локальный rollback-тег в `image`, задайте `pull_policy: never`
   и запустите `docker compose up -d --no-deps --no-build cli-proxy-api`.
4. Проверьте вход и API. Не восстанавливайте старые auth-файлы поверх обновившихся
   токенов без необходимости; сохраните также данные после неудачного обновления.

При последующих обновлениях сначала скачивайте новый digest, сохраняйте текущий
образ и конфигурацию, затем меняйте `CLI_PROXY_IMAGE` и пересоздавайте сервис.
