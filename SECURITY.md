# Security policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability.

Use GitHub's private vulnerability reporting for this repository:

1. Open the repository's **Security** tab.
2. Select **Advisories**.
3. Select **Report a vulnerability**.

Include the affected version, deployment mode, reproduction steps, impact, and
any suggested mitigation. You can expect an acknowledgement within three
business days and a status update within seven business days. Remediation and
disclosure timing depend on severity and whether a safe upgrade is available.

If private vulnerability reporting is unavailable, contact the maintainer
through the private contact method listed on the
[GitHub profile](https://github.com/Franklin-Osede). Do not include sensitive
cluster data in the initial message.

## Supported versions

Until the project reaches 1.0, security fixes are provided for the latest
published release only.

## Security scope

KTM intentionally records Deployment declarative state and ConfigMap contents.
ConfigMaps may contain credentials or internal configuration even when they
should not. Treat the storage directory or PVC as confidential.

KTM does not request access to Kubernetes Secrets by default. Reports about
unexpected Secret access, privilege escalation, unsafe rollback behavior,
snapshot disclosure, path traversal, or tampering with persisted history are
in scope.
