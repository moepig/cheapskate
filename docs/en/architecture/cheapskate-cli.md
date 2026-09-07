# CLI architecture

`cheapskate-cli` exposes `list`, `show`, `schedule`, `override`, `clear-override`, and `remove`. The CLI and web console call the same group application service, so validation and conditional-write behavior are identical.

The `-output text|json` option selects output. When the list read succeeds, JSON `list` emits one object with `groups` and `errors`. Text `list` sends valid groups to stdout and invalid groups to stderr.

Exit code `0` means success, `1` means an argument, AWS, DynamoDB, or internal failure, and `2` means invalid stored configuration or a conditional-write conflict.
