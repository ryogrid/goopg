package executor

import (
	"fmt"
	"strings"
)

// cmdtag_table.go — the full PostgreSQL command-tag table
// (postgres/src/include/tcop/cmdtaglist.h), reduced to the two flags
// CREATE EVENT TRIGGER's WHEN TAG IN (...) filter validates against:
// eventTriggerOK (validate_ddl_tags -> command_tag_event_trigger_ok) and
// tableRewriteOK (validate_table_rewrite_tags -> command_tag_table_rewrite_ok).
// postgres/src/backend/tcop/cmdtag.c. Keyed upper-case; lookups are
// case-insensitive to mirror GetCommandTagEnum's pg_strcasecmp bsearch.
var commandTagBehavior = map[string]struct {
	eventTriggerOK bool
	tableRewriteOK bool
}{
	"ALTER ACCESS METHOD":              {true, false},
	"ALTER AGGREGATE":                  {true, false},
	"ALTER CAST":                       {true, false},
	"ALTER COLLATION":                  {true, false},
	"ALTER CONSTRAINT":                 {true, false},
	"ALTER CONVERSION":                 {true, false},
	"ALTER DATABASE":                   {false, false},
	"ALTER DEFAULT PRIVILEGES":         {true, false},
	"ALTER DOMAIN":                     {true, false},
	"ALTER EVENT TRIGGER":              {false, false},
	"ALTER EXTENSION":                  {true, false},
	"ALTER FOREIGN DATA WRAPPER":       {true, false},
	"ALTER FOREIGN TABLE":              {true, false},
	"ALTER FUNCTION":                   {true, false},
	"ALTER INDEX":                      {true, false},
	"ALTER LANGUAGE":                   {true, false},
	"ALTER LARGE OBJECT":               {true, false},
	"ALTER MATERIALIZED VIEW":          {true, true},
	"ALTER OPERATOR":                   {true, false},
	"ALTER OPERATOR CLASS":             {true, false},
	"ALTER OPERATOR FAMILY":            {true, false},
	"ALTER POLICY":                     {true, false},
	"ALTER PROCEDURE":                  {true, false},
	"ALTER PUBLICATION":                {true, false},
	"ALTER ROLE":                       {false, false},
	"ALTER ROUTINE":                    {true, false},
	"ALTER RULE":                       {true, false},
	"ALTER SCHEMA":                     {true, false},
	"ALTER SEQUENCE":                   {true, false},
	"ALTER SERVER":                     {true, false},
	"ALTER STATISTICS":                 {true, false},
	"ALTER SUBSCRIPTION":               {true, false},
	"ALTER SYSTEM":                     {false, false},
	"ALTER TABLE":                      {true, true},
	"ALTER TABLESPACE":                 {false, false},
	"ALTER TEXT SEARCH CONFIGURATION":  {true, false},
	"ALTER TEXT SEARCH DICTIONARY":     {true, false},
	"ALTER TEXT SEARCH PARSER":         {true, false},
	"ALTER TEXT SEARCH TEMPLATE":       {true, false},
	"ALTER TRANSFORM":                  {true, false},
	"ALTER TRIGGER":                    {true, false},
	"ALTER TYPE":                       {true, true},
	"ALTER USER MAPPING":               {true, false},
	"ALTER VIEW":                       {true, false},
	"ANALYZE":                          {false, false},
	"BEGIN":                            {false, false},
	"CALL":                             {false, false},
	"CHECKPOINT":                       {false, false},
	"CLOSE":                            {false, false},
	"CLOSE CURSOR":                     {false, false},
	"CLOSE CURSOR ALL":                 {false, false},
	"CLUSTER":                          {false, false},
	"COMMENT":                          {true, false},
	"COMMIT":                           {false, false},
	"COMMIT PREPARED":                  {false, false},
	"COPY":                             {false, false},
	"COPY FROM":                        {false, false},
	"CREATE ACCESS METHOD":             {true, false},
	"CREATE AGGREGATE":                 {true, false},
	"CREATE CAST":                      {true, false},
	"CREATE COLLATION":                 {true, false},
	"CREATE CONSTRAINT":                {true, false},
	"CREATE CONVERSION":                {true, false},
	"CREATE DATABASE":                  {false, false},
	"CREATE DOMAIN":                    {true, false},
	"CREATE EVENT TRIGGER":             {false, false},
	"CREATE EXTENSION":                 {true, false},
	"CREATE FOREIGN DATA WRAPPER":      {true, false},
	"CREATE FOREIGN TABLE":             {true, false},
	"CREATE FUNCTION":                  {true, false},
	"CREATE INDEX":                     {true, false},
	"CREATE LANGUAGE":                  {true, false},
	"CREATE MATERIALIZED VIEW":         {true, false},
	"CREATE OPERATOR":                  {true, false},
	"CREATE OPERATOR CLASS":            {true, false},
	"CREATE OPERATOR FAMILY":           {true, false},
	"CREATE POLICY":                    {true, false},
	"CREATE PROCEDURE":                 {true, false},
	"CREATE PUBLICATION":               {true, false},
	"CREATE ROLE":                      {false, false},
	"CREATE ROUTINE":                   {true, false},
	"CREATE RULE":                      {true, false},
	"CREATE SCHEMA":                    {true, false},
	"CREATE SEQUENCE":                  {true, false},
	"CREATE SERVER":                    {true, false},
	"CREATE STATISTICS":                {true, false},
	"CREATE SUBSCRIPTION":              {true, false},
	"CREATE TABLE":                     {true, false},
	"CREATE TABLE AS":                  {true, false},
	"CREATE TABLESPACE":                {false, false},
	"CREATE TEXT SEARCH CONFIGURATION": {true, false},
	"CREATE TEXT SEARCH DICTIONARY":    {true, false},
	"CREATE TEXT SEARCH PARSER":        {true, false},
	"CREATE TEXT SEARCH TEMPLATE":      {true, false},
	"CREATE TRANSFORM":                 {true, false},
	"CREATE TRIGGER":                   {true, false},
	"CREATE TYPE":                      {true, false},
	"CREATE USER MAPPING":              {true, false},
	"CREATE VIEW":                      {true, false},
	"DEALLOCATE":                       {false, false},
	"DEALLOCATE ALL":                   {false, false},
	"DECLARE CURSOR":                   {false, false},
	"DELETE":                           {false, false},
	"DISCARD":                          {false, false},
	"DISCARD ALL":                      {false, false},
	"DISCARD PLANS":                    {false, false},
	"DISCARD SEQUENCES":                {false, false},
	"DISCARD TEMP":                     {false, false},
	"DO":                               {false, false},
	"DROP ACCESS METHOD":               {true, false},
	"DROP AGGREGATE":                   {true, false},
	"DROP CAST":                        {true, false},
	"DROP COLLATION":                   {true, false},
	"DROP CONSTRAINT":                  {true, false},
	"DROP CONVERSION":                  {true, false},
	"DROP DATABASE":                    {false, false},
	"DROP DOMAIN":                      {true, false},
	"DROP EVENT TRIGGER":               {false, false},
	"DROP EXTENSION":                   {true, false},
	"DROP FOREIGN DATA WRAPPER":        {true, false},
	"DROP FOREIGN TABLE":               {true, false},
	"DROP FUNCTION":                    {true, false},
	"DROP INDEX":                       {true, false},
	"DROP LANGUAGE":                    {true, false},
	"DROP MATERIALIZED VIEW":           {true, false},
	"DROP OPERATOR":                    {true, false},
	"DROP OPERATOR CLASS":              {true, false},
	"DROP OPERATOR FAMILY":             {true, false},
	"DROP OWNED":                       {true, false},
	"DROP POLICY":                      {true, false},
	"DROP PROCEDURE":                   {true, false},
	"DROP PUBLICATION":                 {true, false},
	"DROP ROLE":                        {false, false},
	"DROP ROUTINE":                     {true, false},
	"DROP RULE":                        {true, false},
	"DROP SCHEMA":                      {true, false},
	"DROP SEQUENCE":                    {true, false},
	"DROP SERVER":                      {true, false},
	"DROP STATISTICS":                  {true, false},
	"DROP SUBSCRIPTION":                {true, false},
	"DROP TABLE":                       {true, false},
	"DROP TABLESPACE":                  {false, false},
	"DROP TEXT SEARCH CONFIGURATION":   {true, false},
	"DROP TEXT SEARCH DICTIONARY":      {true, false},
	"DROP TEXT SEARCH PARSER":          {true, false},
	"DROP TEXT SEARCH TEMPLATE":        {true, false},
	"DROP TRANSFORM":                   {true, false},
	"DROP TRIGGER":                     {true, false},
	"DROP TYPE":                        {true, false},
	"DROP USER MAPPING":                {true, false},
	"DROP VIEW":                        {true, false},
	"EXECUTE":                          {false, false},
	"EXPLAIN":                          {false, false},
	"FETCH":                            {false, false},
	"GRANT":                            {true, false},
	"GRANT ROLE":                       {false, false},
	"IMPORT FOREIGN SCHEMA":            {true, false},
	"INSERT":                           {false, false},
	"LISTEN":                           {false, false},
	"LOAD":                             {false, false},
	"LOCK TABLE":                       {false, false},
	"LOGIN":                            {true, false},
	"MERGE":                            {false, false},
	"MOVE":                             {false, false},
	"NOTIFY":                           {false, false},
	"PREPARE":                          {false, false},
	"PREPARE TRANSACTION":              {false, false},
	"REASSIGN OWNED":                   {false, false},
	"REFRESH MATERIALIZED VIEW":        {true, false},
	"REINDEX":                          {true, false},
	"RELEASE":                          {false, false},
	"RESET":                            {false, false},
	"REVOKE":                           {true, false},
	"REVOKE ROLE":                      {false, false},
	"ROLLBACK":                         {false, false},
	"ROLLBACK PREPARED":                {false, false},
	"SAVEPOINT":                        {false, false},
	"SECURITY LABEL":                   {true, false},
	"SELECT":                           {false, false},
	"SELECT FOR KEY SHARE":             {false, false},
	"SELECT FOR NO KEY UPDATE":         {false, false},
	"SELECT FOR SHARE":                 {false, false},
	"SELECT FOR UPDATE":                {false, false},
	"SELECT INTO":                      {true, false},
	"SET":                              {false, false},
	"SET CONSTRAINTS":                  {false, false},
	"SHOW":                             {false, false},
	"START TRANSACTION":                {false, false},
	"TRUNCATE TABLE":                   {false, false},
	"UNLISTEN":                         {false, false},
	"UPDATE":                           {false, false},
	"VACUUM":                           {false, false},
}

// validateDDLTags mirrors validate_ddl_tags (event_trigger.c): every WHEN TAG
// IN (...) value on a ddl_command_start/ddl_command_end/sql_drop event
// trigger must be both a recognized command tag (42601 if not) and one PG
// allows event triggers to fire for (0A000 if recognized but disallowed,
// e.g. "VACUUM").
func validateDDLTags(tags []string, pos int) error {
	for _, tag := range tags {
		behavior, known := commandTagBehavior[strings.ToUpper(tag)]
		if !known {
			return &ExecError{Code: "42601", Pos: pos, Message: fmt.Sprintf("filter value %q not recognized for filter variable \"tag\"", tag)}
		}
		if !behavior.eventTriggerOK {
			return &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("event triggers are not supported for %s", tag)}
		}
	}
	return nil
}

// validateTableRewriteTags mirrors validate_table_rewrite_tags: unlike
// validate_ddl_tags it does not special-case an unrecognized tag (an unknown
// tag resolves to CMDTAG_UNKNOWN, whose table_rewrite_ok is false) — both an
// unknown tag and a known-but-not-table-rewrite tag get the same 0A000.
func validateTableRewriteTags(tags []string, pos int) error {
	for _, tag := range tags {
		if behavior := commandTagBehavior[strings.ToUpper(tag)]; !behavior.tableRewriteOK {
			return &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("event triggers are not supported for %s", tag)}
		}
	}
	return nil
}
