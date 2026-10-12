import { Field } from "@astilba/ui/field";
import { Input } from "@astilba/ui/input";
import { Textarea } from "@astilba/ui/textarea";

export const FieldExample = () => (
  <>
    <Field.Root>
      <Field.Label htmlFor="project-name">Project name</Field.Label>
      <Input
        aria-describedby="project-name-help"
        defaultValue="astilba-app"
        id="project-name"
        name="projectName"
      />
      <Field.Description id="project-name-help">
        Used for the package name.
      </Field.Description>
      <Field.Error hidden id="project-name-error">
        Project names must be portable.
      </Field.Error>
    </Field.Root>
    <Field.Root>
      <Field.Label htmlFor="project-description">Description</Field.Label>
      <Textarea
        aria-describedby="project-description-help"
        defaultValue="A useful project."
        id="project-description"
        name="description"
        rows={3}
      />
      <Field.Description id="project-description-help">
        Describe the project briefly.
      </Field.Description>
    </Field.Root>
    <Field.Root data-invalid="">
      <Field.Label htmlFor="github-owner">GitHub owner</Field.Label>
      <Input
        aria-describedby="github-owner-error"
        aria-invalid="true"
        defaultValue="invalid--owner"
        id="github-owner"
        name="githubOwner"
      />
      <Field.Error id="github-owner-error">
        Use a valid GitHub account name.
      </Field.Error>
    </Field.Root>
  </>
);
