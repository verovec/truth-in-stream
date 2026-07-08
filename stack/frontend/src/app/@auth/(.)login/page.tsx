import { LoginForm } from "@/app/login/_components/login-form";
import { LoginModal } from "@/app/login/_components/login-modal";
import { getDictionary } from "@/lib/i18n/dictionaries";
import { resolveRequestLocale } from "@/lib/i18n/request";

// Intercepts /login when reached by client-side navigation (the landing page's
// "Open the app" link), rendering the shared login form inside a modal over the
// page. A hard load or refresh of /login bypasses this and renders the
// standalone page instead. The locale follows the same preference-cookie +
// Accept-Language resolution as the standalone page.
export default async function LoginModalPage() {
  const locale = await resolveRequestLocale();
  const dict = await getDictionary(locale);
  return (
    <LoginModal
      copy={{
        title: dict.login.modalTitle,
        intro: dict.login.modalIntro,
        close: dict.login.close,
      }}
    >
      <LoginForm copy={dict.login} />
    </LoginModal>
  );
}
