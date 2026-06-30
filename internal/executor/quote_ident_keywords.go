package executor

// pgReservedQuoteKeywords is the set of SQL keywords that quote_identifier()
// must always double-quote because their category is NOT UNRESERVED_KEYWORD
// (i.e. RESERVED_KEYWORD, TYPE_FUNC_NAME_KEYWORD, or COL_NAME_KEYWORD).
//
// Mirrors PostgreSQL's quote_identifier() in
// src/backend/utils/adt/ruleutils.c, which does:
//
//	int kwnum = ScanKeywordLookup(ident, &ScanKeywords);
//	if (kwnum >= 0 && ScanKeywordCategories[kwnum] != UNRESERVED_KEYWORD)
//	    safe = false;
//
// The list is derived verbatim from src/include/parser/kwlist.h (PG 18.3),
// keeping every entry whose category is not UNRESERVED_KEYWORD. Keywords are
// stored lowercase; quote_identifier only consults this set after confirming
// the identifier is already all-lowercase, so a case-insensitive lookup is
// unnecessary. UNRESERVED_KEYWORDs (e.g. "name", "type", "version") are
// deliberately absent: they need no quoting.
var pgReservedQuoteKeywords = map[string]struct{}{
	"all":               {}, // RESERVED_KEYWORD
	"analyse":           {}, // RESERVED_KEYWORD
	"analyze":           {}, // RESERVED_KEYWORD
	"and":               {}, // RESERVED_KEYWORD
	"any":               {}, // RESERVED_KEYWORD
	"array":             {}, // RESERVED_KEYWORD
	"as":                {}, // RESERVED_KEYWORD
	"asc":               {}, // RESERVED_KEYWORD
	"asymmetric":        {}, // RESERVED_KEYWORD
	"authorization":     {}, // TYPE_FUNC_NAME_KEYWORD
	"between":           {}, // COL_NAME_KEYWORD
	"bigint":            {}, // COL_NAME_KEYWORD
	"binary":            {}, // TYPE_FUNC_NAME_KEYWORD
	"bit":               {}, // COL_NAME_KEYWORD
	"boolean":           {}, // COL_NAME_KEYWORD
	"both":              {}, // RESERVED_KEYWORD
	"case":              {}, // RESERVED_KEYWORD
	"cast":              {}, // RESERVED_KEYWORD
	"char":              {}, // COL_NAME_KEYWORD
	"character":         {}, // COL_NAME_KEYWORD
	"check":             {}, // RESERVED_KEYWORD
	"coalesce":          {}, // COL_NAME_KEYWORD
	"collate":           {}, // RESERVED_KEYWORD
	"collation":         {}, // TYPE_FUNC_NAME_KEYWORD
	"column":            {}, // RESERVED_KEYWORD
	"concurrently":      {}, // TYPE_FUNC_NAME_KEYWORD
	"constraint":        {}, // RESERVED_KEYWORD
	"create":            {}, // RESERVED_KEYWORD
	"cross":             {}, // TYPE_FUNC_NAME_KEYWORD
	"current_catalog":   {}, // RESERVED_KEYWORD
	"current_date":      {}, // RESERVED_KEYWORD
	"current_role":      {}, // RESERVED_KEYWORD
	"current_schema":    {}, // TYPE_FUNC_NAME_KEYWORD
	"current_time":      {}, // RESERVED_KEYWORD
	"current_timestamp": {}, // RESERVED_KEYWORD
	"current_user":      {}, // RESERVED_KEYWORD
	"dec":               {}, // COL_NAME_KEYWORD
	"decimal":           {}, // COL_NAME_KEYWORD
	"default":           {}, // RESERVED_KEYWORD
	"deferrable":        {}, // RESERVED_KEYWORD
	"desc":              {}, // RESERVED_KEYWORD
	"distinct":          {}, // RESERVED_KEYWORD
	"do":                {}, // RESERVED_KEYWORD
	"else":              {}, // RESERVED_KEYWORD
	"end":               {}, // RESERVED_KEYWORD
	"except":            {}, // RESERVED_KEYWORD
	"exists":            {}, // COL_NAME_KEYWORD
	"extract":           {}, // COL_NAME_KEYWORD
	"false":             {}, // RESERVED_KEYWORD
	"fetch":             {}, // RESERVED_KEYWORD
	"float":             {}, // COL_NAME_KEYWORD
	"for":               {}, // RESERVED_KEYWORD
	"foreign":           {}, // RESERVED_KEYWORD
	"freeze":            {}, // TYPE_FUNC_NAME_KEYWORD
	"from":              {}, // RESERVED_KEYWORD
	"full":              {}, // TYPE_FUNC_NAME_KEYWORD
	"grant":             {}, // RESERVED_KEYWORD
	"greatest":          {}, // COL_NAME_KEYWORD
	"group":             {}, // RESERVED_KEYWORD
	"grouping":          {}, // COL_NAME_KEYWORD
	"having":            {}, // RESERVED_KEYWORD
	"ilike":             {}, // TYPE_FUNC_NAME_KEYWORD
	"in":                {}, // RESERVED_KEYWORD
	"initially":         {}, // RESERVED_KEYWORD
	"inner":             {}, // TYPE_FUNC_NAME_KEYWORD
	"inout":             {}, // COL_NAME_KEYWORD
	"int":               {}, // COL_NAME_KEYWORD
	"integer":           {}, // COL_NAME_KEYWORD
	"intersect":         {}, // RESERVED_KEYWORD
	"interval":          {}, // COL_NAME_KEYWORD
	"into":              {}, // RESERVED_KEYWORD
	"is":                {}, // TYPE_FUNC_NAME_KEYWORD
	"isnull":            {}, // TYPE_FUNC_NAME_KEYWORD
	"join":              {}, // TYPE_FUNC_NAME_KEYWORD
	"json":              {}, // COL_NAME_KEYWORD
	"json_array":        {}, // COL_NAME_KEYWORD
	"json_arrayagg":     {}, // COL_NAME_KEYWORD
	"json_exists":       {}, // COL_NAME_KEYWORD
	"json_object":       {}, // COL_NAME_KEYWORD
	"json_objectagg":    {}, // COL_NAME_KEYWORD
	"json_query":        {}, // COL_NAME_KEYWORD
	"json_scalar":       {}, // COL_NAME_KEYWORD
	"json_serialize":    {}, // COL_NAME_KEYWORD
	"json_table":        {}, // COL_NAME_KEYWORD
	"json_value":        {}, // COL_NAME_KEYWORD
	"lateral":           {}, // RESERVED_KEYWORD
	"leading":           {}, // RESERVED_KEYWORD
	"least":             {}, // COL_NAME_KEYWORD
	"left":              {}, // TYPE_FUNC_NAME_KEYWORD
	"like":              {}, // TYPE_FUNC_NAME_KEYWORD
	"limit":             {}, // RESERVED_KEYWORD
	"localtime":         {}, // RESERVED_KEYWORD
	"localtimestamp":    {}, // RESERVED_KEYWORD
	"merge_action":      {}, // COL_NAME_KEYWORD
	"national":          {}, // COL_NAME_KEYWORD
	"natural":           {}, // TYPE_FUNC_NAME_KEYWORD
	"nchar":             {}, // COL_NAME_KEYWORD
	"none":              {}, // COL_NAME_KEYWORD
	"normalize":         {}, // COL_NAME_KEYWORD
	"not":               {}, // RESERVED_KEYWORD
	"notnull":           {}, // TYPE_FUNC_NAME_KEYWORD
	"null":              {}, // RESERVED_KEYWORD
	"nullif":            {}, // COL_NAME_KEYWORD
	"numeric":           {}, // COL_NAME_KEYWORD
	"offset":            {}, // RESERVED_KEYWORD
	"on":                {}, // RESERVED_KEYWORD
	"only":              {}, // RESERVED_KEYWORD
	"or":                {}, // RESERVED_KEYWORD
	"order":             {}, // RESERVED_KEYWORD
	"out":               {}, // COL_NAME_KEYWORD
	"outer":             {}, // TYPE_FUNC_NAME_KEYWORD
	"overlaps":          {}, // TYPE_FUNC_NAME_KEYWORD
	"overlay":           {}, // COL_NAME_KEYWORD
	"placing":           {}, // RESERVED_KEYWORD
	"position":          {}, // COL_NAME_KEYWORD
	"precision":         {}, // COL_NAME_KEYWORD
	"primary":           {}, // RESERVED_KEYWORD
	"real":              {}, // COL_NAME_KEYWORD
	"references":        {}, // RESERVED_KEYWORD
	"returning":         {}, // RESERVED_KEYWORD
	"right":             {}, // TYPE_FUNC_NAME_KEYWORD
	"row":               {}, // COL_NAME_KEYWORD
	"select":            {}, // RESERVED_KEYWORD
	"session_user":      {}, // RESERVED_KEYWORD
	"setof":             {}, // COL_NAME_KEYWORD
	"similar":           {}, // TYPE_FUNC_NAME_KEYWORD
	"smallint":          {}, // COL_NAME_KEYWORD
	"some":              {}, // RESERVED_KEYWORD
	"substring":         {}, // COL_NAME_KEYWORD
	"symmetric":         {}, // RESERVED_KEYWORD
	"system_user":       {}, // RESERVED_KEYWORD
	"table":             {}, // RESERVED_KEYWORD
	"tablesample":       {}, // TYPE_FUNC_NAME_KEYWORD
	"then":              {}, // RESERVED_KEYWORD
	"time":              {}, // COL_NAME_KEYWORD
	"timestamp":         {}, // COL_NAME_KEYWORD
	"to":                {}, // RESERVED_KEYWORD
	"trailing":          {}, // RESERVED_KEYWORD
	"treat":             {}, // COL_NAME_KEYWORD
	"trim":              {}, // COL_NAME_KEYWORD
	"true":              {}, // RESERVED_KEYWORD
	"union":             {}, // RESERVED_KEYWORD
	"unique":            {}, // RESERVED_KEYWORD
	"user":              {}, // RESERVED_KEYWORD
	"using":             {}, // RESERVED_KEYWORD
	"values":            {}, // COL_NAME_KEYWORD
	"varchar":           {}, // COL_NAME_KEYWORD
	"variadic":          {}, // RESERVED_KEYWORD
	"verbose":           {}, // TYPE_FUNC_NAME_KEYWORD
	"when":              {}, // RESERVED_KEYWORD
	"where":             {}, // RESERVED_KEYWORD
	"window":            {}, // RESERVED_KEYWORD
	"with":              {}, // RESERVED_KEYWORD
	"xmlattributes":     {}, // COL_NAME_KEYWORD
	"xmlconcat":         {}, // COL_NAME_KEYWORD
	"xmlelement":        {}, // COL_NAME_KEYWORD
	"xmlexists":         {}, // COL_NAME_KEYWORD
	"xmlforest":         {}, // COL_NAME_KEYWORD
	"xmlnamespaces":     {}, // COL_NAME_KEYWORD
	"xmlparse":          {}, // COL_NAME_KEYWORD
	"xmlpi":             {}, // COL_NAME_KEYWORD
	"xmlroot":           {}, // COL_NAME_KEYWORD
	"xmlserialize":      {}, // COL_NAME_KEYWORD
	"xmltable":          {}, // COL_NAME_KEYWORD
}
