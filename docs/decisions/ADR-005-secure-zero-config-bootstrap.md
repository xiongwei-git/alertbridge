# ADR-005: Secure zero-config container bootstrap

## Status

Accepted

## Date

2026-07-15

## Context

Requiring operators to clone the source repository, copy JSON, create several host secret files, align group IDs and preconfigure clients made a small prebuilt image unnecessarily difficult to deploy. The desired flow is one Compose file, one operator-selected administrator password, and all business configuration after startup. The simplification cannot introduce a shared default password, expose credentials through container environment metadata, or place the dynamic-configuration key in the SQLite volume.

## Decision

- Runtime and security defaults are compiled into the binary. Production startup no longer reads JSON configuration.
- Compose sources the administrator bootstrap password from the host environment as a Docker Compose Secret and mounts it at `/run/secrets/admin_password`; the value is not placed in the service environment.
- The bootstrap username defaults to `admin`. Passwords must contain 16–1024 bytes and have no line break or NUL.
- On an empty database, the password is converted to an Argon2id PHC string using 64 MiB, three passes, one lane, a random 128-bit salt and a 256-bit tag. Login verification is serialized to prevent concurrent memory-exhaustion attacks. Parameter parsing is bounded before allocation. The design follows the memory-constrained Argon2id direction in [RFC 9106](https://www.rfc-editor.org/rfc/rfc9106.html).
- Once a credential exists, bootstrap username and password inputs are ignored; they never overwrite the database credential.
- A random 256-bit AES master key is generated atomically on first startup, stored with mode `0600` in a dedicated `alertbridge-secrets` volume, and reused on every restart.
- Clients, channels and routes start empty. They can only be created through the authenticated management console.
- `alertbridge-data` and `alertbridge-secrets` are an inseparable backup and restore pair.

## Alternatives Considered

### Fixed default administrator password

Rejected because every fresh deployment would share a known credential and operators routinely fail to rotate defaults.

### Password directly in the container environment

Rejected because container configuration and inspection APIs retain environment values. Compose Secrets keep the value out of service environment metadata.

### Store or derive the AES key inside the SQLite database

Rejected because database-only theft would then disclose both ciphertext and the key needed to decrypt it. Deriving the key from the login password would also couple password rotation to data recovery and make offline guessing more valuable.

### Keep JSON as an optional compatibility path

Rejected by product decision. A single empty-state control plane is easier to reason about, document and test than two competing configuration authorities.

## Consequences

- A new server needs only `compose.yaml`, `.env` and Docker; it does not need the repository or build toolchain.
- Changing the bootstrap password after initialization does not change the stored administrator credential.
- The key volume must be backed up with the database volume. Losing either prevents a complete recovery.
- The first login deliberately consumes memory for password hashing, but login verification is serialized and the Compose memory limit remains 128 MiB.
- There is no static recovery password. Operators must protect their selected password and encrypted backups.
