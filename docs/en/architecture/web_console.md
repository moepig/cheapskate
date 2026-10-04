# Web console

The optional web console provides a group list, group details, a schedule form, and an override form. Group pages show the fixed membership tag, resource configuration tags, and current state from read-only Describe calls.

The server uses `DEFAULT_TIMEZONE` to display and parse override expiry. An unset value means UTC, and an invalid value prevents startup.

All requests validate Host against an allowlist fixed when the server is constructed, before routing or accessing state. Matching is case-insensitive and includes any port. An empty allowlist rejects every request. This prevents an unlisted domain from accessing the console through DNS rebinding. The executable configures this list with ALLOWED_HOSTS and defaults to loopback hosts with the listen port.

Mutation forms additionally accept only same-origin or none when Sec-Fetch-Site is present, and compare the Origin host with the request Host when Origin is present. Requests without either header still require an allowed Host. The server provides no authentication; access restrictions belong in the ingress. Conditional-write conflicts return HTTP 409. Security headers restrict content, framing, referrers, and form destinations.
