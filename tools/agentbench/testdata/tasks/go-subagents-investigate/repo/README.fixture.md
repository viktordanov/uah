# shop

Three small packages of a shop's back end:

- `pricing`: order totals, discount codes, and tax.
- `inventory`: stock levels and reservations.
- `schedule`: when recurring jobs run next.

Run the tests with `go test ./...`. Each package has slow tests that talk to a simulated service.
