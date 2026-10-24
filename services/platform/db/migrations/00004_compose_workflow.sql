-- +goose Up
ALTER TABLE environments DROP CONSTRAINT environments_configuration_owner_check;
ALTER TABLE environments ADD CONSTRAINT environments_configuration_owner_check CHECK(configuration_owner IN ('dashboard','compose'));
-- +goose StatementBegin
CREATE FUNCTION fixed_configuration_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.configuration_owner <> OLD.configuration_owner THEN
  RAISE EXCEPTION 'configuration ownership requires an explicit conversion';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER fixed_configuration_owner BEFORE UPDATE ON environments FOR EACH ROW EXECUTE FUNCTION fixed_configuration_owner();

-- +goose Down
DROP TRIGGER fixed_configuration_owner ON environments;
DROP FUNCTION fixed_configuration_owner();
ALTER TABLE environments DROP CONSTRAINT environments_configuration_owner_check;
ALTER TABLE environments ADD CONSTRAINT environments_configuration_owner_check CHECK(configuration_owner = 'dashboard');
