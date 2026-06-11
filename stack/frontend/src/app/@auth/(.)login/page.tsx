import { LoginForm } from "@/app/login/_components/login-form";
import { LoginModal } from "@/app/login/_components/login-modal";

// Intercepts /login when reached by client-side navigation (the landing page's
// "Open the app" link), rendering the shared login form inside a modal over the
// page. A hard load or refresh of /login bypasses this and renders the
// standalone page instead.
export default function LoginModalPage() {
  return (
    <LoginModal>
      <LoginForm />
    </LoginModal>
  );
}
