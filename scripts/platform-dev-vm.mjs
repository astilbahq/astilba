// Which Colima VM belongs to this checkout's stack, and what one run may do to it.
// state.vm is unset until the stack starts creating a profile, "creating" while that first
// start has not claimed it, and true once the VM carries this stack's marker. A run deletes
// only a VM with this stack's marker, or an unmarked one it was creating, and stops only a
// VM it verified, started or created.
export function stackVM({ vm, state, saveState }) {
  let owned = false;
  let started = false;
  let created = false;
  const deletable = async () => {
    const marker = await vm.marker();
    return marker === "ours" || (marker === "none" && state.vm === "creating");
  };
  const start = async (status) => {
    started = status !== "Running";
    await vm.start();
  };
  const create = async () => {
    created = true;
    await vm.create();
  };

  return {
    // For --reset and --wipe. Says whether the VM was deleted; one another stack has
    // claimed is left alone.
    async remove() {
      const status = await vm.status();
      if (status === "absent") return true;
      await start(status);
      if (!(await deletable())) return false;
      console.log(`Deleting Colima profile ${vm.profile}…`);
      await vm.remove();
      started = false;
      return true;
    },
    // Creates, recreates or starts the VM, then checks it carries this stack's marker.
    async bring() {
      const status = await vm.status();
      if (status === "absent") {
        if (state.initialized)
          throw new Error(
            "The local stack's VM is missing; start over with --reset",
          );
        // Recorded first, so an interrupted creation can be redone or wiped later.
        state.vm = "creating";
        saveState();
        console.log(
          `Creating Colima profile ${vm.profile}; the first start takes a few minutes…`,
        );
        try {
          await create();
        } catch (error) {
          // "creating" names only a profile this checkout brought into existence.
          if ((await vm.status({ cleanup: true })) === "absent") {
            delete state.vm;
            saveState();
          }
          throw error;
        }
      } else if (state.vm === "creating") {
        // An interrupted creation may have stopped part way through provisioning, and a
        // VM that was never claimed holds nothing to keep, so it is made again.
        await start(status);
        if (!(await deletable()))
          throw new Error(
            `Colima profile ${vm.profile} was claimed by another stack after this one's first start was interrupted; choose another --vm profile, or --wipe to forget it`,
          );
        console.log(
          `Creating Colima profile ${vm.profile} again; its first start was interrupted…`,
        );
        await vm.remove();
        started = false;
        await create();
      } else {
        if (!state.vm)
          throw new Error(
            `Colima profile ${vm.profile} already exists and was not created by this stack; choose another --vm profile`,
          );
        console.log(`Starting Colima profile ${vm.profile}…`);
        await start(status);
      }
      if (state.vm === "creating") {
        await vm.claim();
        state.vm = true;
        saveState();
      }
      if ((await vm.marker()) !== "ours")
        throw new Error(
          `Colima profile ${vm.profile} belongs to another stack; refusing to change it`,
        );
      owned = true;
    },
    // On the way out. The VM keeps its data and workloads, which start again with it. One
    // this run tried to create is stopped unless another stack won the race to claim it.
    async release() {
      if ((await vm.status({ cleanup: true })) !== "Running") return;
      if (
        owned ||
        started ||
        (created && (await vm.marker({ cleanup: true })) !== "other")
      ) {
        console.log(`Stopping Colima profile ${vm.profile}…`);
        await vm.stop();
      }
    },
  };
}
