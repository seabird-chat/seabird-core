fn main() {
    // NOTE: we need to use configure rather than the simple case because both
    // seabird and seabird_chat_ingest use the same package name. This also gives
    // us the option to disable client generation so we do that.
    tonic_build::configure()
        .build_client(false)
        .build_server(true)
        .compile(
            &["../proto/seabird.proto", "../proto/seabird_chat_ingest.proto"],
            &["../proto"],
        )
        .unwrap();
}
