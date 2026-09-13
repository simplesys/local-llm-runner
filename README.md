# locallm

Терминальный кодинг-агент для локальных LLM: работа с кодом в стиле Claude Code, но на моделях, запущенных на вашей машине через [LM Studio](https://lmstudio.ai).

> **Статус: в разработке.** Работает end-to-end: выбор и переключение модели, потоковый диалог, инструменты (чтение и правка файлов, поиск, shell), песочница, подтверждение изменяющих действий, метрики и история сессий. Интерфейс построчный, полноэкранный — запланирован. Что именно реализовано — в [`docs/project_description.md`](docs/project_description.md).

## Требования

- Go — версия из `go.mod`
- LM Studio с запущенным локальным сервером (`lms server start`, порт 1234)
- Для разработки установите: [golangci-lint v2](https://golangci-lint.run) (`brew install golangci-lint`)

## Сборка и запуск

```sh
make build
./bin/locallm --help
./bin/locallm --model qwen2.5-coder
./bin/locallm --task "add tests for package config"
```

| Параметр | Флаг | Переменная окружения | По умолчанию |
|---|---|---|---|
| Модель | `--model` | `LOCAL_MODEL` | уже загруженная модель с `tool_use` |
| Адрес API | `--base-url` | `LOCAL_LLM_BASE_URL` | `http://localhost:1234/v1` |
| Разовая задача | `--task` | — | интерактивный режим |
| Рабочая директория | `--workspace` | — | каталог запуска |
| Список моделей | `--list-models` | — | — |
| Политика песочницы | `--sandbox` | `LOCAL_LLM_SANDBOX` | `best-effort` |
| Автоподтверждение | `--yes` | — | выключено |

Полный список флагов — `locallm --help`, описание каждого — [`docs/project_description.md`](docs/project_description.md), раздел 7.

Команды интерактивного режима: `/help`, `/model [id]`, `/models`, `/metrics`, `/clear`, `/exit`.

## Разработка

```sh
make help    # список команд
make check   # tidy + lint + tests — должно быть зелёным перед коммитом
```

- [`docs/project_description.md`](docs/project_description.md) — требования и текущее состояние
- [`docs/technical_description.md`](docs/technical_description.md) — устройство проекта и технические решения
- [`docs/code_style.md`](docs/code_style.md) — стиль кода
- [`docs/archives/`](docs/archives) — журналы продуктовых и технических решений
- [`AGENTS.md`](AGENTS.md) — правила для облачных ИИ-агентов
- [`docs/local_llm_agents_en.md`](docs/local_llm_agents_en.md) — правила для локальных моделей (на английском: их читает сама модель)

## Лицензия

[MIT](LICENSE)
