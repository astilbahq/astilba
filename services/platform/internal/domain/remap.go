package domain

import (
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// RemapSpecification copies a visual specification under new identities: ids maps every
// application, volume, binding and route ID, and secrets every secret version it references.
// A secret version missing from secrets is refused as invalid input in field, the area the
// copy came from, since the caller built secrets from what it could find. Routes keep their
// domains; the caller decides what a copied route may publish.
func RemapSpecification(source Specification, ids, secrets map[uuid.UUID]uuid.UUID, field string) (Specification, error) {
	// Deep-copy pointer and slice fields before changing the source.
	raw, err := stablejson.Marshal(source)
	if err != nil {
		return Specification{}, err
	}
	s, _, err := ParseSpec(raw)
	if err != nil {
		return s, err
	}
	if s.Compose != nil || len(s.Networks) > 0 || s.Placement != nil {
		return s, Invalid(field + ".specification")
	}
	remap := func(id *uuid.UUID) error {
		if v := ids[*id]; v != uuid.Nil {
			*id = v
			return nil
		}
		return errors.New("incomplete identity map")
	}
	secret := func(id *uuid.UUID) error {
		if v := secrets[*id]; v != uuid.Nil {
			*id = v
			return nil
		}
		return Invalid(field + ".secretVersionId")
	}
	variables := func(values []Variable) error {
		for i := range values {
			if values[i].SecretVersionID != nil {
				if err := secret(values[i].SecretVersionID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = variables(s.Variables); err != nil {
		return s, err
	}
	for i := range s.Applications {
		a := &s.Applications[i]
		if err = remap(&a.ID); err != nil {
			return s, err
		}
		if err = variables(a.Variables); err != nil {
			return s, err
		}
		for j := range a.Mounts {
			if err = remap(&a.Mounts[j].VolumeID); err != nil {
				return s, err
			}
		}
		for j := range a.DependsOn {
			if err = remap(&a.DependsOn[j]); err != nil {
				return s, err
			}
		}
		if a.Postgres != nil {
			if err = secret(&a.Postgres.PasswordSecretVersionID); err != nil {
				return s, err
			}
		}
	}
	for i := range s.Volumes {
		if err = remap(&s.Volumes[i].ID); err != nil {
			return s, err
		}
	}
	for i := range s.Bindings {
		b := &s.Bindings[i]
		for _, id := range []*uuid.UUID{&b.ID, &b.SourceID, &b.TargetID} {
			if err = remap(id); err != nil {
				return s, err
			}
		}
	}
	for i := range s.Routes {
		r := &s.Routes[i]
		if err = remap(&r.ID); err != nil {
			return s, err
		}
		if err = remap(&r.TargetID); err != nil {
			return s, err
		}
	}
	return s, nil
}
