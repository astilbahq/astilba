package deployment

import "github.com/google/uuid"

func (p Plan) InstanceIDs(id uuid.UUID) []uuid.UUID {
	if p.Local != nil {
		return append([]uuid.UUID(nil), p.Local.Instances[id.String()]...)
	}
	return append([]uuid.UUID{p.Instance(id)}, p.ReplicaInstances[id.String()]...)
}

// PrepareInstances freezes desired runtime identities before image resolution.
// Protected releases replace only their changed application's complete pool.
func (p *Plan) PrepareInstances(previous Plan) {
	if p.Specification.Compose != nil {
		return
	}
	if p.Specification.HasReplicaConfiguration() || previous.Recipe == ReplicaRecipe || previous.Recipe == CrossHostRecipe {
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
		if previous.Local != nil && previous.ServerID == p.ServerID && app.Postgres == nil {
			if ids := previous.InstanceIDs(app.ID); len(ids) > 0 {
				p.Instances[id] = ids[0]
			}
		}
		for i := 0; i < app.ReplicaCount()-1; i++ {
			instance := uuid.New()
			if previous.ServerID == p.ServerID && i < len(previous.ReplicaInstances[id]) {
				instance = previous.ReplicaInstances[id][i]
			}
			if previous.Local != nil && previous.ServerID == p.ServerID {
				if ids := previous.InstanceIDs(app.ID); i+1 < len(ids) {
					instance = ids[i+1]
				}
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
