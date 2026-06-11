// Client-side navigation keeps a slot's last match visible until another route
// matches it. This catch-all matches every non-intercepted route to null so the
// login modal closes when navigation lands anywhere other than /login.
export default function CatchAll() {
  return null;
}
