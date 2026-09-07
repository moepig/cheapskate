# Web console

The optional web console provides a group list, group details, a schedule form, and an override form. Group pages show the fixed membership tag, resource configuration tags, and current state from read-only Describe calls.

The server uses `DEFAULT_TIMEZONE` to display and parse override expiry. An unset value means UTC, and an invalid value prevents startup.

Mutation forms accept only same-origin or none when Sec-Fetch-Site is present, and compare the Origin host with the request Host when Origin is present. Requests without either header are accepted. The server provides no authentication; access restrictions belong in the ingress. Conditional-write conflicts return HTTP 409. Security headers restrict content, framing, referrers, and form destinations.
