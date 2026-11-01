-- A read-only role for analysis: it may select every store table and view
-- and do nothing else. Activation sets its password.
CREATE ROLE cliapiproxy_reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;

GRANT USAGE ON SCHEMA public TO cliapiproxy_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO cliapiproxy_reader;

-- Tables and views later migrations create are readable too.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO cliapiproxy_reader;
