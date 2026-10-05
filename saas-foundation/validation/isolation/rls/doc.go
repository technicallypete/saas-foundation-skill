// Package rls holds the PostgreSQL row-level security, privilege and audit
// isolation cases (plan Appendix A). Every case is a test in this package and
// obtains its connection from exactly one guarded internal/fixture helper:
//
//	TestRuntime_*   fixture.RuntimeDB / RuntimeDBSingle (application role)
//	TestOwner_*     fixture.OwnerTx (migrator login, SET LOCAL ROLE owner)
//	TestRetention_* fixture.RetentionDB (audit-retention role)
//	TestControl_*   fixture.SuperuserSetup or a deliberately unsafe setting;
//	                reported separately and never counted as an assertion
//
// Negative cases are paired with a positive control on the same connection,
// and assert the blocking mechanism (hidden row, WITH CHECK, missing
// privilege, constraint or trigger) rather than "some error". Every test
// leaves the database unchanged.
package rls
