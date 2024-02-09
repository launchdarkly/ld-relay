---
title: It's time to update supported Go versions
assignees: 'launchdarkly/team-sdk'
labels: ''
---
It's time to update Relay's supported Go versions, due to a recent upstream Go release.

The Go major release cadence is ~every 6 months; the two most recent major versions are supported. 
Note that between major releases, the Go team often ships multiple minor versions. 

|             | Current repo configuration         | Desired repo configuration                                                                                          |
|-------------|------------------------------------|---------------------------------------------------------------------------------------------------------------------|
| Latest      | {{ env.RELAY_LATEST_VERSION}}      | [{{ env.OFFICIAL_LATEST_VERSION }}](https://go.dev/doc/devel/release#go{{ env.OFFICIAL_LATEST_VERSION }})           |
| Penultimate | {{ env.RELAY_PENULTIMATE_VERSION}} | [{{ env.OFFICIAL_PENULTIMATE_VERSION }}](https://go.dev/doc/devel/release#go{{ env.OFFICIAL_PENULTIMATE_VERSION }}) |



Run locally:
```bash
./scripts/update-go-release-version.sh {{ env.OFFICIAL_LATEST_VERSION }} {{ env.OFFICIAL_PENULTIMATE_VERSION }}
```
