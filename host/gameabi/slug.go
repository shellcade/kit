package gameabi

// ValidateBareName reports whether slug is a valid bare game name — the
// lower-case kebab-case rule (`^[a-z0-9-]{1,32}$`) every declared meta.slug
// must satisfy at load time. Exported so author-facing tooling
// (`shellcade-kit new`) can reject an invalid name at scaffold time with the
// same error text the loader would produce at the first `check`, instead of
// after a game has been built around the slug.
func ValidateBareName(slug string) error { return validateBareName(slug) }
