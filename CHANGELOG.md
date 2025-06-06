# Recent changes

* New configuration parameters `checkpoint_segment_size` and
  `max_poll_interval` can be used to fine-tune the stream processor.

* The folio-analytics and folio-reshare tags are no longer set in new
  installations.  The ALTER SYSTEM command can be used to set them via
  the configuration parameters `external_sql_folio` and
  `external_sql_reshare`.

# v1.4.0-rc1

* JSON transformation has been extended to include objects and arrays,
  and it is now disabled by default in new installations.  The `CREATE
  DATA MAPPING` command can be used to enable and configure
  transformation of specific JSON paths.  The `LIST data_mappings`
  command shows configured mappings.

* New commands `GRANT` and `REVOKE` improve support for managing user
  privileges.

* A new command `DROP USER` supports removing users.

* New commands `ALTER SYSTEM` and `LIST config` support defining and
  viewing server configuration parameters.

* A new command `CREATE SCHEMA` supports creating a user schema for an
  existing user.

* New commands `REGISTER USER` and `DEREGISTER USER` support adding an
  existing database user to a Metadb instance.

* A new function `mdbusers()` lists registered users.

* A new data source option `map_public_schema` supports changing the
  schema name of tables that originate in the `public` schema.

* The command `CREATE DATA ORIGIN` no longer requires restarting the
  server before it takes effect.

* The compilation script `build.sh` has been renamed to `build`.

* The folio-analytics tag has been updated to v1.8.0.

* Go 1.24 is now required to build Metadb.

* Various performance improvements and bug fixes.
