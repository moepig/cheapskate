# Web console

The optional web console provides a group list, group details, a schedule form, and an override form. Group pages show the fixed membership tag, resource configuration tags, and current state from read-only Describe calls.

The server uses `DEFAULT_TIMEZONE` to display and parse override expiry. An unset value means UTC, and an invalid value prevents startup.

Mutation forms require a same-origin request. Conditional-write conflicts return HTTP 409. Security headers restrict content, framing, referrers, and form destinations.
