\set ON_ERROR_STOP on
SELECT current_setting('server_version_num')::integer/10000 AS major_version;
SELECT count(*) AS note_count,min(id) AS first_id,max(id) AS last_id FROM comparison_notes;
SELECT md5(string_agg(row_to_json(t)::text,E'\n' ORDER BY id)) AS notes_hash
 FROM (SELECT id,slug,body,attributes::text,created_at FROM comparison_notes) AS t;
SELECT md5(string_agg(row_to_json(t)::text,E'\n' ORDER BY note_id)) AS links_hash
 FROM (SELECT note_id,amount FROM comparison_links) AS t;
SELECT pg_get_serial_sequence('comparison_notes','id') AS identity_sequence;
SELECT last_value,is_called FROM comparison_notes_id_seq;
SELECT extname,extversion FROM pg_extension ORDER BY extname;
SELECT conname,pg_get_constraintdef(oid) FROM pg_constraint
 WHERE conrelid IN ('comparison_notes'::regclass,'comparison_links'::regclass) ORDER BY conname;
SELECT tablename,tableowner FROM pg_tables WHERE schemaname='public' ORDER BY tablename;
SELECT grantee,table_name,privilege_type FROM information_schema.role_table_grants
 WHERE table_schema='public' ORDER BY grantee,table_name,privilege_type;
SELECT rolname,rolsuper,rolinherit,rolcreaterole,rolcreatedb,rolcanlogin,rolreplication,rolbypassrls,rolconnlimit,rolconfig
 FROM pg_roles WHERE rolname='comparison_reader';
SELECT granted.rolname AS granted_role,member.rolname AS member_role,m.admin_option
 FROM pg_auth_members AS m JOIN pg_roles AS granted ON granted.oid=m.roleid
 JOIN pg_roles AS member ON member.oid=m.member
 WHERE granted.rolname='comparison_reader' OR member.rolname='comparison_reader'
 ORDER BY granted.rolname,member.rolname;
