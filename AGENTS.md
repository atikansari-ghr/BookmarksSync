# Repository Guidelines

## Project Structure & Module Organization

This directory is the starting workspace for BookmarksSync. No source code, tests, assets, package manifests, or Git metadata currently exist. Update this guide as the project takes shape.

When adding code, group related modules by responsibility and keep tests close to the code they verify or in a clearly named `tests/` directory. Place static assets in an `assets/` directory when needed. Document the actual layout in the README once it exists.

## Build, Test, and Development Commands

No build, test, lint, or local development commands are configured yet. Do not assume commands such as `npm test` or `npm run build` are available.

When introducing a toolchain, provide reproducible setup instructions and record the exact commands here and in the README. Include dependency installation, local execution, testing, and production builds where applicable.

## Coding Style & Naming Conventions

No language, indentation standard, formatter, or linter has been selected. Follow the surrounding code once conventions exist; avoid unrelated formatting changes. Choose descriptive names that reflect each module's responsibility.

When selecting a language, add its formatter and lint configuration, document indentation and naming rules, and use those tools consistently.

## Testing Guidelines

No testing framework or coverage threshold is configured. Add automated tests alongside meaningful behavior changes, including relevant edge cases and failure paths. Use descriptive test names that state the expected behavior. Document the framework, test file naming pattern, and execution command when the first tests are introduced.

## Commit & Pull Request Guidelines

No Git history is available to establish a commit convention. Use concise, imperative commit subjects, such as `Add bookmark import validation`. Keep changes focused.

Pull requests should explain the change, link relevant issues, and report validation performed. Include screenshots for visible interface changes. Identify untested behavior explicitly.

## Attribution & Local Drafts

Use `atikansari <atikansari@ansariz.biz>` as the Git author and credit Atik Ansari alone in published material. Do not add assistant coauthor trailers or generation notices. License copyright lines must use `Copyright (c) <year> Atik Ansari`.

Keep social media drafts in `linkedin/`. Never commit or push them, and never add that directory to a repository's `.gitignore`; it is excluded globally.