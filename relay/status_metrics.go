package relay

import (
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
)

// statusSnapshot reads everything the status endpoint reports, for the status instruments to
// observe. It satisfies metrics.StatusSourceFunc.
//
// It is built by the same code that serves the endpoint, which is the point: a metric that said
// healthy while the document said degraded would be worse than no metric. The cost is that a
// collection performs the same big segment store query the endpoint does -- refer to
// buildEnvironmentStatus.
func (r *Relay) statusSnapshot() metrics.StatusSnapshot {
	envs, healthy := r.collectEnvironmentStatuses()

	snapshot := metrics.StatusSnapshot{
		Healthy:      healthy,
		AutoConfig:   r.buildAutoConfigStatus(),
		Environments: make([]metrics.EnvironmentStatusSnapshot, 0, len(envs)),
	}
	for _, env := range envs {
		// env.name, not env.key: the instruments report the display name that the request metrics
		// carry, so that status series and traffic series join.
		snapshot.Environments = append(snapshot.Environments, metrics.EnvironmentStatusSnapshot{
			Name: env.name,
			Rep:  env.rep,
		})
	}
	return snapshot
}
