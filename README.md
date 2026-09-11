# local-llm-runner

Терминальный кодинг-агент для локальных LLM: работа с кодом в стиле Claude Code, но на моделях, запущенных на вашей машине через [LM Studio](https://lmstudio.ai).

> **Статус: ранняя разработка.** Готовы CLI и конфигурация; агентский цикл в работе. Что реализовано — в [`docs/project_description.md`](docs/project_description.md).

## Требования

- Go — версия из `go.mod`
- LM Studio с запущенным локальным сервером (`lms server start`, порт 1234)
- Для разработки установите: [golangci-lint v2](https://golangci-lint.run) (`brew install golangci-lint`)

## Сборка и запуск

```sh
make build
./bin/local-llm-runner --help
./bin/local-llm-runner --model qwen2.5-coder
./bin/local-llm-runner --task "add tests for package config"
```

| Параметр | Флаг | Переменная окружения | По умолчанию |
|---|---|---|---|
| Модель | `--model` | `LOCAL_MODEL` | `ornith-1.5-35b-a3b` |
| Адрес API | `--base-url` | `LOCAL_LLM_BASE_URL` | `http://localhost:1234/v1` |
| Разовая задача | `--task` | — | интерактивный режим |

## Разработка

```sh
make help    # список команд
make check   # tidy + lint + tests — должно быть зелёным перед коммитом
```

- [`docs/project_description.md`](docs/project_description.md) — требования и текущее состояние
- [`docs/technical_description.md`](docs/technical_description.md) — устройство проекта и технические решения
- [`docs/code_style.md`](docs/code_style.md) — стиль кода
- [`docs/archives/`](docs/archives) — журналы продуктовых и технических решений
- [`AGENTS.md`](AGENTS.md) — правила для ИИ-агентов

## Лицензия

[MIT](LICENSE)
