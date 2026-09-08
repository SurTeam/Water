// Keep the dedicated server executable on the exact same startup path as the
// compatibility `water server` command. `main.rs` detects this binary name and
// enters server mode without requiring a synthetic subcommand argument.
include!("../main.rs");
