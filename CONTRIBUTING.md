### Commit Message Convention

Commit messages must follow this format:

`<type>(<scope>): <description>`

- `<type>` describes the type of change, such as `feat`, `fix`, `refactor`, `build`, etc.
- `<scope>` must be:
  - `server` — for server-side changes
  - `client` — for client-side changes
  - `plugin` — for plugin-related changes
  - `all` — when changes involve multiple parts (`server`, `client`, and/or `plugin`)
- `<description>` must be **concise but complete**, clearly covering **all changes made in the commit**. Do not omit important changes for the sake of brevity.