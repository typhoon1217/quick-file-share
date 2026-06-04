# Security

Quick File Share is intended for trusted intranet use. Do not expose it directly to the public internet without a reverse proxy, TLS, and an access password.

## Baseline rules

- Keep `data/`, binaries, logs, `.env` files, systemd units, archives, and deploy scratch files out of git.
- Use `QFS_ACCESS_PASSWORD` when the service is reachable outside a trusted LAN.
- Put the service behind TLS when browsers outside localhost will use the password.
- Set `QFS_PUBLIC_BASE_URL` to the externally reachable HTTPS URL when running behind a reverse proxy.
- Rotate the access password if it was shared in chat, logs, shell history, or a support ticket.
- Treat delete URLs as secrets. Anyone with the delete token can remove the item.
- Store runtime data in a host-managed directory such as `/srv/quick-file-share`, not inside the source checkout.
- Back up only if the deployment needs recovery. The default behavior is temporary sharing with expiry.

## Reporting

Report security issues privately to the repository owner instead of opening a public issue with exploit details.
