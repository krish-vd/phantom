# phantom

**Watch the package names AI models invent, and know when someone registers one.**

AI coding assistants regularly suggest `pip install` and `npm install` commands for packages that don't exist. The same fake names come back again and again, across prompts and across models. Anyone can register one of those names on PyPI or npm and wait for an AI to send developers there. That attack is called *slopsquatting*.

Research has measured this in one-off snapshots, and scanners check your own project. Phantom does what neither does: it **keeps watching**. It records every name models invent, checks the registries every day, and raises an alert the moment one of those names gets registered.

## Why it needs to keep watching

In May 2026, a public dataset listed `fastapi-turbo`, `pandas-easy-pivot` and `torch-lightning-easy` among the package names AI models hallucinate most. On 21 November 2026, Phantom found all three on PyPI:

```
$ phantom seed --source dataset:depscope --since 2026-05-04 examples/seeds.txt
claimed   pypi/fastapi-turbo
claimed   pypi/pandas-easy-pivot
claimed   pypi/torch-lightning-easy

Watching 3 names from dataset:depscope. Run phantom watch on a schedule.
```

`phantom check` shows each one was first published on 2026-07-28, 2026-10-22 and 2026-10-29 respectively, each with a single release, 0.1.0.

Registering a name after it is listed doesn't make it malicious. Some of these will be genuine projects, or defensive registrations. But a name that models keep recommending, and that appeared only after a public list said it didn't exist, is exactly what should be reviewed before anyone installs it. A snapshot can't tell you that. A watch can.

It also shows why Phantom **never publishes unclaimed names** (see [Responsible disclosure](#responsible-disclosure)).

## Install

```
go install github.com/krish-vd/phantom@latest
```

A single static binary. No dependencies, no API key.

In a terminal the output is in colour, with live progress and panels. Piped into a file or a CI log it is plain text, and `NO_COLOR=1` turns colour off everywhere.

## Use it

**Watch names right now, no model needed**

```
phantom seed --source dataset:depscope examples/seeds.txt   # lines like: pypi/fastapi-turbo 2026-05-04
phantom watch                                                          # exit code 3 when a name gets claimed
```

**Find what your own models invent (free, local)**

```
ollama pull qwen2.5-coder:7b
phantom probe --model qwen2.5-coder:7b --samples 3
```

Phantom prints each prompt and flags invented names as it goes, then a summary line with the model's hallucination rate and the number of unique invented names. Everything is saved to `phantom-data/phantom.json`.

Works with Ollama, or any OpenAI-compatible server (LM Studio, llama.cpp, vLLM) with `--provider openai --endpoint http://localhost:1234/v1`. Set `PHANTOM_API_KEY` only if your endpoint needs one.

`--samples` matters: a name a model invents every time is far more dangerous than a one-off.

**Protect a project**

```
$ phantom guard requirements.txt
FAIL pypi/pandas-easy-pivot        registered 2026-10-22, after AI models were already inventing it
FAIL pypi/fastapi-retry-magic-zq9  does not exist on the registry

Checked 3 dependencies from requirements.txt: 2 failed, 0 warnings.
```

With no arguments it finds `requirements.txt`, `pyproject.toml` and `package.json`. Packages published in the last 30 days get a warning (`--min-age`).

In CI:

```yaml
- uses: krish-vd/phantom@v0
```

**See everything at once**

```
phantom dash
```

The watch list as one status bar, every claimed name with how long after it was first seen missing it got registered, and each probed model's hallucination rate. Unclaimed names stay hidden unless you pass `--show-unclaimed`, so the dashboard is safe to screenshot.

**Report**

```
phantom report --public --out REPORT.md --feed feed.json
phantom check pypi/fastapi-turbo npm/react-hooks-essential
```

## How it classifies names

| Status | Meaning |
|---|---|
| `unclaimed` | Models suggest it; it doesn't exist. Anyone can register it. |
| `claimed` | It didn't exist when models were first seen inventing it, and now it does. **Review before installing.** |
| `young` | It exists, but was published less than 90 days before models started suggesting it. |
| `real` | An established package that existed long before. |

Phantom reads install commands only from code (fenced blocks and `inline code`), so prose like "pip install the requests library" doesn't produce a package called `the`. Python imports are mapped to their distribution names (`cv2` → `opencv-python`, `sklearn` → `scikit-learn`) and standard-library modules are ignored.

## Run it as a public watch

`.github/workflows/watch.yml` runs `phantom watch` daily and a small open model weekly on the free GitHub runner, using Ollama on CPU. It commits only the public report and the blocklist feed, and opens an issue when a name is claimed. The database, which contains unclaimed names, stays in the Actions cache and is never committed.

## Responsible disclosure

A list of names that models invent and nobody owns is a shopping list for attackers. So:

- `phantom report --public` and `feed.json` include only names that are **already registered**. Defenders need those to block installs; attackers gain nothing from them.
- Unclaimed names stay in your local database. If you find names that several models invent repeatedly, send them privately to the PyPI and npm security teams, who can block or reserve them.

## Limits

- PyPI returns "not found" both for names nobody has registered and for names it forbids, so a few `unclaimed` names can't actually be registered.
- `claimed` means "registered after models invented it", not "malicious". Phantom flags names for review; it doesn't inspect package code.
- Probing measures the models you run. Results from small local models are not results for frontier models.

## Roadmap

- Inspect newly claimed packages: install scripts, network calls, and how similar they are to the real package
- More registries: crates.io, RubyGems, Go modules
- A shared public feed that many watchers contribute to

## License

MIT
