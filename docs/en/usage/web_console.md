# Web console

This document explains how to inspect configuration, change schedules and overrides, and configure public access to the web console.

## Pages and observations

The optional web console provides these pages:

| Page | Content |
| --- | --- |
| Group list | Stored group configuration and indefinite override controls |
| Group detail | Schedule, override, ECS limit, membership tag, resource tags, and current AWS state |
| Schedule form | Start and stop cron configuration |
| Override form | running, stopped, or disabled and optional expiration |

Current-state inspection is read-only. Opening a page does not start or stop resources.

## Changing configuration

Supply both cron expressions in the schedule form. In the override form, select a state and set Until for a timed override. Group detail initializes Until to two hours after page rendering; clear it for an indefinite override. Overrides from the list page are indefinite.

Dates are displayed and parsed in DEFAULT_TIMEZONE, defaulting to UTC. Saved configuration takes effect in a later reconciliation cycle. Reload the page to inspect configuration and current state.

HTTP 409 indicates a conflicting configuration change. Reload, check configuration, and explicitly repeat the intended change. For configuration constraints, see [Group configuration](group_config.md).

## Publishing and access settings

The console has no built-in authentication. Place it behind an authenticated API Gateway or equivalent ingress and restrict access. Configure public host values with ALLOWED_HOSTS.

Every request validates Host. Mutation forms accept same-origin or none when Sec-Fetch-Site is present and verify the request Host against Origin when Origin is present. Unlisted hosts and cross-origin mutation requests are rejected.

For ALLOWED_HOSTS matching, BASE_PATH forwarding, and listen ports, see [Runtime configuration](config.md#web-console). For deployment, see [Setup](setup.md).
