# Directory publication selection v1

Directory publication selection lets a trusted localhost client define which comic archives in an already discovered directory belong to one logical publication. The client selects resources; Readium still parses the resources and produces the manifest, table of contents, and positions.

## Capability and registration

`GET /_readium/capabilities` advertises version `1` under `directoryPublicationSelection.versions`. The endpoint is available only to loopback clients.

Register a selection with `POST /_readium/directory-selections`:

```json
{
  "publication": { "path": "Series" },
  "selection": {
    "version": 1,
    "readingOrder": ["Gamma_Chapter_1.cbz", "Gamma_Chapter_2.cbz"]
  }
}
```

The response contains an opaque `id`, `version`, deterministic `digest`, and `expiresAt` Unix timestamp in milliseconds. Add the opaque id as the `readiumSelection` query parameter when opening that directory publication. A selected response acknowledges the applied selection in `Readium-Directory-Selection: v1; digest=<digest>`. Delete the registration with `DELETE /_readium/directory-selections/{id}` when its owner expires or closes.

Registrations live for at most 24 hours, are bound to their normalized publication path, and are handles rather than publication identities. Publication caching uses the normalized path plus the selection digest.

## Normalization and validation

- The publication path is a decoded, slash-separated path relative to the configured local directory; `.` represents the local-directory root.
- Reading-order entries are relative URL HREFs exactly as Readium discovers them. Each HREF is decoded once, validated as a relative path, then canonically percent-encoded for matching.
- Empty selections, duplicate canonical HREFs, absolute paths, backslashes, repeated separators, queries, fragments, traversal, and unknown resources are rejected.
- Selection applies only when the target is a directory whose discovered resources form a comic-archive publication. Individual archives and loose-image publications are unaffected.

The digest is SHA-256 over length-prefixed UTF-8 values. Each value is prefixed by its unsigned 64-bit big-endian byte length. Values are `directory-publication-selection-v1`, then every canonical reading-order HREF in order. The external form is `sha256:<lowercase hex>`. The publication cache key combines the normalized publication path with this digest.
