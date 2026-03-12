contributing

thanks for wanting to help — short, practical guide.

setup
- copy .env.example -> .env and fill provider keys (spotify, soundcloud, genius, database, jwt)
- go mod download

development workflow
- create a feature branch off main; name: feat/<short>-<what>
- run the server locally: go run ./bedrock_server

code style
- format go code with `gofmt`/`goimports` before committing
- follow the single-package layout used in this repo
- keep comments short and lowercase; explain intent only where non-obvious

tests & lint
- run integration suite: go run ./tests/
- run platform entry tests: go run ./tests/spotify  (or ./tests/youtube, ./tests/auth)
- run the project linter (powershell): ./linter.ps1

commits & pull requests
- stage only the files you intend to change
- commit messages: short header line, 1-2 sentence why in body
- when creating a PR, target the main branch; include a short summary and a brief test plan
- do not force-push to main

safety & rate limits
- avoid parallel requests that could hit provider rate limits in CI or test environments
- when adding code that touches external services, add retries, timeouts, and reasonable defaults

risky actions (ask first)
- deleting branches, force-push, replacing db migrations, or changing CI; ask a maintainer before doing these

questions & contact
- open an issue or ping the repo maintainers with context and a short reproduction

thanks — keep changes small and focused. if unsure, ask first.
