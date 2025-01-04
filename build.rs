fn main() {
    println!("cargo:rerun-if-changed=migrations");

    let protos_path = std::env::var("SEABIRD_PROTO_PATH").unwrap_or_else(|_| "./proto".to_string());

    // NOTE: we need to use configure rather than the simple case because both
    // seabird and seabird_chat_ingest use the same package name. This also gives
    // us the option to disable client generation so we do that.
    tonic_build::configure()
        .build_client(false)
        .build_server(true)
        .compile(
            &["common.proto", "seabird.proto", "seabird_chat_ingest.proto"],
            &[&protos_path],
        )
        .unwrap();
}
