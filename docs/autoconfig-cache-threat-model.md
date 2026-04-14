# AutoConfig cache encryption — threat model

This document describes what Relay’s AutoConfig **cache encryption** is intended to protect, and what it does **not** address. It complements the configuration reference for [`cacheKey`](./configuration.md#file-section-autoconfig) and `cacheEncryptionKey`.

## Assets at risk

The AutoConfig cache holds a **snapshot of environment metadata** from LaunchDarkly’s automatic configuration stream: environment IDs, names, project identifiers, and **credential material** (SDK keys, mobile keys, and related rotation state) as delivered by the control plane.

That data is written to a **shared persistence tier** you operate (Redis/Valkey or DynamoDB). Typical exposure paths include:

- **At-rest disclosure**: backups, exports, replicas, or misconfigured access control on the datastore.
- **Cross-tenant or over-broad access**: operators or applications with read access to the key/table who should not see raw LaunchDarkly credentials.
- **Accidental leakage**: logs, support bundles, or screen shares that include stored payloads.

## Security goals

| Goal | Mechanism |
|------|-----------|
| **Confidentiality at rest** | Each cached item is encrypted with **AES-256-GCM** before write. The encryption key is derived with **SHA-256** from `cacheEncryptionKey`, or from the AutoConfig key if `cacheEncryptionKey` is unset (see [configuration](./configuration.md)). |
| **Integrity of cached blobs** | GCM authentication rejects ciphertext that was truncated or tampered with in the store. |

## Out of scope (non-goals)

Encryption **does not** replace these controls:

| Scenario | Why encryption is insufficient |
|----------|--------------------------------|
| **Compromised Relay host** | The running process holds the AutoConfig key and derived encryption material in memory; an attacker with code execution can decrypt cache entries or call LaunchDarkly APIs. |
| **Anyone who knows the AutoConfig key** (when using default key derivation) | The same secret used to authenticate to the AutoConfig stream is used to derive the cache encryption key if `cacheEncryptionKey` is omitted. Anyone with that key can decrypt cached payloads offline. Use a **separate** `cacheEncryptionKey` when people or systems may see the AutoConfig key but should not decrypt historical cache data. |
| **Malicious Relay binary** | A modified binary could exfiltrate keys before encryption or skip encryption. |
| **Transport to LaunchDarkly** | TLS protects the stream; cache encryption is unrelated to TLS. |
| **Authorization inside your datastore** | You must still restrict which principals can read/write the cache namespace (Redis hash key / DynamoDB partition). |

## Threat summary

We optimize for **honest-but-curious or sloppy access to the persistence layer**: readers of Redis or DynamoDB should not gain usable LaunchDarkly credentials from cache blobs without the encryption secret. We **do not** model the Relay process or the AutoConfig bearer token as untrusted.

## Operational recommendations

1. Prefer setting **`cacheEncryptionKey`** to a secret **different from** the AutoConfig key if the AutoConfig key is broadly distributed.
2. Restrict datastore IAM / Redis ACLs so only Relay instances need read/write access to the cache namespace.
3. Treat backups of the cache like **credential backups**: store and rotate encryption material accordingly.
