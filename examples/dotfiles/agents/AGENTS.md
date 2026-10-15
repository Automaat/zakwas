# Working agreements

- Read the code before changing it; prefer small, reviewable diffs.
- Run the project's tests before saying a change works.
- Never print, log or commit secrets.
{{ if eq .Provider "claude" }}
- Use the Skill tool for the `review` skill.
{{ end }}
