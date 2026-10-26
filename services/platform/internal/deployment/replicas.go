package deployment

import "github.com/google/uuid"

func (p Plan) InstanceIDs(id uuid.UUID) []uuid.UUID {
	return append([]uuid.UUID{p.Instance(id)}, p.ReplicaInstances[id.String()]...)
}

// PrepareInstances freezes desired runtime identities before image resolution.
// Protected releases replace only their changed application's complete pool.
func (p *Plan) PrepareInstances(previous Plan) {
	if p.Specification.Compose != nil {
		return
	}
	if p.Specification.HasReplicaConfiguration() || previous.Recipe == ReplicaRecipe {
		p.Recipe = ReplicaRecipe
	}
	p.Instances = map[string]uuid.UUID{}
	p.ReplicaInstances = map[string][]uuid.UUID{}
	for _, app := range p.Specification.Applications {
		id := app.ID.String()
		if previous.ServerID == p.ServerID {
			if instance := previous.Instances[id]; instance != uuid.Nil && app.Postgres == nil {
				p.Instances[id] = instance
			}
		}
		for i := 0; i < app.ReplicaCount()-1; i++ {
			instance := uuid.New()
			if previous.ServerID == p.ServerID && i < len(previous.ReplicaInstances[id]) {
				instance = previous.ReplicaInstances[id][i]
			}
			p.ReplicaInstances[id] = append(p.ReplicaInstances[id], instance)
		}
	}
}

func (p *Plan) ReplaceInstances(id uuid.UUID) {
	if p.Instances == nil {
		p.Instances = map[string]uuid.UUID{}
	}
	p.Instances[id.String()] = uuid.New()
	for i := range p.ReplicaInstances[id.String()] {
		p.ReplicaInstances[id.String()][i] = uuid.New()
	}
}
