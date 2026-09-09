fn main() -> anyhow::Result<()> {
    water::server::run(std::env::args().skip(1))
}
