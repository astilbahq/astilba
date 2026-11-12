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
		if previous.ServerID == p.ServerID && app.Postgres == nil {
			instance := previous.Instances[id]
			// A placement lists every instance, including an application's own ID for one that
			// never left the primary.
			if ids := previous.InstanceIDs(app.ID); previous.Local != nil && len(ids) > 0 {
				instance = ids[0]
			}
			if instance != uuid.Nil {
				p.keepInstance(app.ID, instance)
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

// keepInstance makes instance the application's first instance. An application's own ID is its
// default instance, which Instance returns for an application left out of Instances, so it is
// written by leaving it out: validation refuses it there, and one identity keeps one encoding.
func (p *Plan) keepInstance(app, instance uuid.UUID) {
	if instance == app {
		delete(p.Instances, app.String())
		return
	}
	p.Instances[app.String()] = instance
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
