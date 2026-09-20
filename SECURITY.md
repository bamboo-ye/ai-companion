# Security

See the scoped [pre-publication review](docs/SECURITY_REVIEW.md). Only the latest
validated default branch is supported; historical commits are not secure releases.

## Report privately

Do not put credentials, private documents, production logs, or exploitable details
in public issues or pull requests. Use this repository's **Security → Report a
vulnerability** entry when available. If private reporting is unavailable, ask a
maintainer for a private contact without posting vulnerability details.

## Local configuration stays local

- Commit only `.env.example` placeholders. Keep actual `.env` files, private keys,
  service-account files, database dumps and raw incident evidence outside Git.
- `.gitignore` and both Docker-context exclusion files protect local secrets.
  They do not remove files already committed or make `git add -f` safe.
- `node scripts/check-public-files.mjs` checks the index for forbidden file types.
  The public-repository safety workflow also scans the available Git history
  with a checksum-pinned scanner and verifies detection with a synthetic token.
- `.gitleaks.toml` permits only reviewed variable-name false positives. Do not
  suppress real findings by allowlisting an entire directory or adding secrets
  to the allowlist.

## Before deployment

- Replace every development credential and generate independent high-entropy
  authentication, gateway, confirmation and operator secrets. Never use a
  `NEXT_PUBLIC_` name for a secret.
- Use production mode, HTTPS, administrator Passkeys/MFA, least-privilege
  accounts, and the intended origin/domain. Do not expose database, cache,
  object-storage or observability ports publicly.
- Rebuild images after dependency/security updates. A source update does not
  patch an already-running container or rotate a deployed secret.
- Review model-provider retention, document access and tool approvals. Uploaded
  material is data, not authorization to run instructions contained in it.
- Re-run dependency and image vulnerability checks for the actual deployment;
  a repository scan is not a penetration test or a guarantee of no vulnerabilities.

## If a credential was committed

Revoke or rotate it first. Removing the current file does not remove Git history,
pull-request refs, Actions logs, release assets, forks or existing clones. Keep
the repository private while containing an incident. Coordinate any required
history rewrite and re-scan the repository and its published artifacts before
changing visibility. Never attach an unredacted scanner report to a public issue.

See [GitHub's sensitive-data removal guidance](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository).
