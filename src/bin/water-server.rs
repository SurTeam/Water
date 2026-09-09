fn main() -> anyhow::Result<()> {
    water::server::init_tracing();
    water::server::run(std::env::args().skip(1))
}
