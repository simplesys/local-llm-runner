---
name: commit-message
description: Составляет сообщение коммита в формате Conventional Commits по текущим изменениям в git и выводит его в чат, ничего не коммитя. Использовать, когда пользователь присылает «коммит», «сообщение для коммита», «сообщение коммита», «текст коммита», «напиши коммит», «commit», «commit message», «conventional commit» или похожую просьбу.
---

# Сообщение для коммита

## Когда срабатывает

Скилл вызывается, когда сообщение пользователя — это просьба дать текст коммита. Триггеры в любом регистре, отдельным сообщением или внутри фразы:

- русские: `коммит`, `сообщение для коммита`, `сообщение коммита`, `текст коммита`, `напиши коммит`;
- английские: `commit`, `commit message`, `conventional commit`.

Если прислан только триггер, без пояснений, — этого достаточно. Уточняющих вопросов не задавай: бери текущие изменения в репозитории.

**Результат всегда один: сообщение в формате [Conventional Commits](https://www.conventionalcommits.org/).**

## Что делать

1. Собери контекст:
   - `git status --short`;
   - `git diff --staged` — если есть проиндексированные изменения, сообщение составляется **только** по ним;
   - иначе `git diff` плюс новые файлы (строки `??` в `git status --short`).
2. Определи тип, область и суть изменения по фактическому diff.
3. Выведи готовое сообщение одним блоком кода, чтобы его можно было скопировать целиком.

## Чего делать нельзя

- Не выполняй `git commit`, `git add`, `git push` и другие изменяющие команды — скилл только печатает текст (см. запреты в `AGENTS.md`).
- Не описывай то, чего нет в diff.
- Не добавляй строки авторства ИИ: `Co-Authored-By`, `Claude-Session`, `Generated with` и подобные. В сообщении только то, что описано в разделе «Формат».
- Не объединяй несколько несвязанных изменений в один коммит.

## Формат

```
<type>(<scope>): <subject>

<body>

<footer>
```

- **type** — обязателен: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.
- **scope** — желателен: затронутый пакет или область — `config`, `llm`, `agent`, `tools`, `metrics`, `ui`, `app`, `cmd`, `docs`, `make`. Если изменение общепроектное, scope опускается.
- **subject** — на английском, в повелительном наклонении (`add`, `fix`, `move`, а не `added` или `adds`), со строчной буквы, без точки в конце, до 72 символов.
- **body** — по необходимости: зачем сделано изменение, а не пересказ diff. Строки до 100 символов.
- **footer** — `BREAKING CHANGE: <описание>` для несовместимых изменений, ссылки на задачи. Несовместимость можно дополнительно пометить `!` после scope.
- Язык сообщения — английский: документация проекта ведётся на русском, код и коммиты — на английском.

## Частные случаи

- **Несколько логических изменений в diff.** Не склеивай их. Предложи разбиение: перечисли группы файлов и дай отдельное сообщение для каждой группы.
- **Изменений нет.** Сообщи, что рабочее дерево чистое, и не придумывай текст.
- **Изменения только в документации.** Тип `docs`, даже если правок много.
- **Код и его тесты вместе.** Один коммит с типом по сути изменения (`feat`, `fix`), тесты отдельным коммитом не выделяются.

## Примеры

```
feat(config): add --base-url flag

Allow pointing the agent at any OpenAI-compatible server instead of the
default LM Studio endpoint. The value is validated as an absolute http(s) URL.
```

```
fix(llm): keep SSE lines longer than 64KB

bufio.Scanner used its default buffer, so long completions were truncated
mid-stream and the response failed to parse.
```

```
docs: describe the session metrics format
```

```
test(config): cover flag and environment priority
```

```
refactor(app)!: pass process environment through app.Options

BREAKING CHANGE: app.Run takes app.Options instead of positional arguments.
```
