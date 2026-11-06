# Проверки бэкенда

Сценарии и проверки получены из кейса Tomorrow School; готовый план другого проекта не использовался.

| Гарантия | Проверка | Вид |
| --- | --- | --- |
| Go AST создаёт три мутации, и каждая выявляется тестами | `TestPrepareThreeRealMutations`, `TestRealGoProjectMutatesOnlyTestDetectableBehavior` | unit + Docker integration |
| Удалённые и локальные ZIP не могут подменять файлы вне проекта | `TestLoadZIPRejectsUnsafeNames`, `TestLoadZIPRejectsSymlinks` | unit |
| GitHub URL ограничен одним доменом и архив привязан к SHA | `TestFetchGitHubPinsCommitAndAcceptsOnlyGitHub`, `TestFetchGitHubRejectsRedirectToExternalHost` | HTTP transport tests |
| Запуск без Docker не превращается в запуск на Windows | `TestDockerRunnerFailsClosedWithoutDocker`, `TestAuditCreationFailsClosedWithoutDocker` | integration |
| Студент не видит карту мутаций, не редактирует тесты, не меняет код после дедлайна | `TestAuditAPISeparatesRolesAndReportsEvidence`, `TestAuditDeadlineFinishesOnServer`, `TestDeadlineReachedAfterAuthenticationStillRejectsEdit` | HTTP integration |
| Запоздалый результат тестов не относится к новой версии кода | `TestInFlightTestsCannotCertifyNewerCode` | concurrency integration |
| Страница, скрипт и стили отдаются с того же origin, а демо создаётся через обычный движок | `TestDemoPageServesSameOriginAssets`, `TestDemoEndpointUsesRealPreparationFlow` | HTTP integration |

Циклы RED → GREEN: первый `go test ./...` не компилировался без `Prepare`/`Analyze`/`Apply`; после движка тесты прошли. То же повторено для `LoadZIP`, `DockerRunner`, `FetchGitHub`, `NewServer`. Тест на гонку у дедлайна сначала возвращал HTTP 200, после повторной проверки времени под блокировкой — HTTP 409. Docker-интеграция обнаружила слишком маленький лимит памяти (256 МиБ); после увеличения до 1 ГиБ прогон с реальными Go-тестами прошёл. Подготовка тестового проекта занимает около трёх минут из-за изолированной холодной сборки каждого кандидата.

Проверки: `go test -short ./...`, `go test -race -short ./...`, `go vet ./...`, `go test -count=1 -coverprofile=coverage.out ./...`. Последний полный прогон после добавления лимита параллельных контейнеров: **85,2%** для основного пакета, **83,8%** по всем Go-пакетам (включая тонкую точку входа `cmd/auditd`). HTTP smoke собранного `bin/auditd.exe` вернул `200 {"status":"ok"}`. После следующих изменений результаты следует перепроверять, а не считать эти цифры вечной гарантией.

Границы: Docker-интеграция проверяет встроенный небольшой Go-модуль на этом компьютере. Большие проекты, дополнительные языки, полноценный интерфейс и сохранение сессий между перезапусками пока не проверяются, потому что они не реализованы. Тестовую страницу следует проверять вручную в браузере поверх API-тестов.
