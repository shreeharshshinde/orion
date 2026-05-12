# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| `main` (unreleased) | ✅ |
| 0.1.x | ✅ |

Older versions receive no security fixes. Please upgrade to the latest release.

---

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report vulnerabilities by opening a [GitHub Security Advisory](https://github.com/shreeharshshinde/orion/security/advisories/new)
(preferred) or emailing the maintainer directly via the email on their GitHub profile. Include:

- A description of the vulnerability and its potential impact.
- Steps to reproduce or a proof-of-concept (if safe to share).
- The version(s) affected.
- Any suggested mitigations you have identified.

You will receive an acknowledgement within **48 hours** and a status update
within **7 days**. We aim to release a patch within **30 days** of a confirmed
critical vulnerability.

We follow [coordinated disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure):
please give us reasonable time to patch before publishing details publicly.

---

## Scope

The following are in scope:

- Authentication and authorization bypasses in the API server.
- SQL injection or data exfiltration via the store layer.
- Privilege escalation in the Kubernetes executor.
- Secrets leaking through logs, metrics, or gRPC responses.
- Denial-of-service via the job submission or streaming endpoints.

The following are **out of scope**:

- Vulnerabilities in third-party dependencies that have no available fix.
- Issues requiring physical access to the host.
- Social engineering attacks.

---

## Security Considerations for Operators

- **Never expose the gRPC port (`:9090`) or Prometheus metrics port (`:9091`)
  to the public internet** without authentication (mTLS or an API gateway).
- Store database credentials and Redis passwords in a secrets manager (e.g.,
  Kubernetes Secrets with external-secrets-operator, AWS Secrets Manager).
  Do not commit `.env` files containing real credentials.
- The Kubernetes executor creates Jobs in the namespace specified by the job
  payload. Restrict which namespaces the worker's ServiceAccount can write to
  using RBAC `RoleBindings` scoped to `orion-jobs` only.
- Enable PostgreSQL SSL (`sslmode=require` or `verify-full`) in production.
  The default `sslmode=disable` in `.env.example` is for local development only.
- Rotate the `ORION_GRPC_SECRET` and database passwords on a regular schedule.

---

## Acknowledgements

We thank all researchers who responsibly disclose vulnerabilities to us.
Confirmed reporters will be credited in the release notes unless they prefer
to remain anonymous.
