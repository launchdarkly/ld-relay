---
title: It's time to update supported Go versions
assignees: ''
labels: ''
---
It's time to update Relay's supported Go versions, due to a recent upstream Go release.

The Go major release cadence is ~every 6 months; the two most recent major versions are supported. 
Note that between major releases, the Go team often ships multiple minor versions. 

|             | Current repo configuration         | Desired repo configuration             |
|-------------|------------------------------------|----------------------------------------|
| Latest      | {{ env.RELAY_LATEST_VERSION}}      | {{ env.OFFICIAL_LATEST_VERSION }}      |
| Penultimate | {{ env.RELAY_PENULTIMATE_VERSION}} | {{ env.OFFICIAL_PENULTIMATE_VERSION }} |



Run locally:
```bash
./scripts/update-go-release-version.sh {{ env.OFFICIAL_LATEST_VERSION }}
```

Then modify `.circle/config.yml`'s `go-previous-version` variable to `{{ env.OFFICIAL_PENULTIMATE_VERSION}}`.
