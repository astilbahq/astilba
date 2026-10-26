import type { InputProps } from "../../components/input";
import { OTPInput } from "../../components/otp-input";

export function AuthenticatorInput({ id, name, required, size }: InputProps) {
  return (
    <OTPInput id={id} name={name} required={required} length={6} size={size} />
  );
}
