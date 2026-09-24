import {
	Alert,
	AlertIcon,
	Button,
	FormControl,
	FormLabel,
	Modal,
	ModalBody,
	ModalCloseButton,
	ModalContent,
	ModalFooter,
	ModalHeader,
	ModalOverlay,
	Popover,
	PopoverBody,
	PopoverContent,
	PopoverHeader,
	PopoverTrigger,
	Progress,
	Stack,
	Text,
	useToast,
} from "@chakra-ui/react";
import {
	ArchiveBoxIcon,
	ArrowDownTrayIcon,
	ArrowUpTrayIcon,
} from "@heroicons/react/24/outline";
import { PanelSelect as Select } from "components/common/PanelSelect";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation } from "react-query";
import {
	exportAntiMageBackup,
	importAntiMageBackup,
	importVPNUIBackup,
	type AntiMageBackupScope,
} from "service/settings";
import { useServicesStore } from "contexts/ServicesContext";
import {
	generateErrorMessage,
	generateSuccessMessage,
} from "utils/toastHandler";
import { FileDropzone } from "./common/FileDropzone";

const buildBackupFilename = (scope: AntiMageBackupScope) => {
	const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
	return `antimage-${scope}-${timestamp}.rbbackup`;
};

type BackupDialog = "import" | "export" | "vpn-ui" | null;

export const DashboardBackupControls = ({
	isBinaryRuntime,
	runtimeLoading,
}: {
	isBinaryRuntime: boolean;
	runtimeLoading: boolean;
}) => {
	const { t } = useTranslation();
	const toast = useToast();
	const borderColor = "panel.border";
	const [isMenuOpen, setMenuOpen] = useState(false);
	const [dialog, setDialog] = useState<BackupDialog>(null);
	const [exportScope, setExportScope] =
		useState<AntiMageBackupScope>("database");
	const [selectedFile, setSelectedFile] = useState<File | null>(null);
	const [uploadProgress, setUploadProgress] = useState<number | null>(null);
	const [vpnUIServiceID, setVPNUIServiceID] = useState("");
	const [duplicatePolicy, setDuplicatePolicy] = useState<"skip" | "rename">("skip");
	const serviceOptions = useServicesStore((state) => state.serviceOptions);
	const fetchServiceOptions = useServicesStore((state) => state.fetchServiceOptions);
	const backupActionsAvailable = isBinaryRuntime && !runtimeLoading;

	useEffect(() => {
		if (dialog === "vpn-ui") void fetchServiceOptions();
	}, [dialog, fetchServiceOptions]);

	const exportMutation = useMutation(exportAntiMageBackup, {
		onSuccess: (blob, scope) => {
			const url = URL.createObjectURL(blob);
			const anchor = document.createElement("a");
			anchor.href = url;
			anchor.download = buildBackupFilename(scope);
			document.body.appendChild(anchor);
			anchor.click();
			anchor.remove();
			URL.revokeObjectURL(url);
			setDialog(null);
			generateSuccessMessage(t("settings.backup.exportReady"), toast);
		},
		onError: (error) => {
			generateErrorMessage(error, toast);
		},
	});

	const importMutation = useMutation(
		(file: File) => importAntiMageBackup(file, setUploadProgress),
		{
			onMutate: () => setUploadProgress(0),
			onSuccess: (result) => {
				generateSuccessMessage(
					t("settings.backup.importDone", {
						tables: result.tables_restored,
						rows: result.rows_restored,
					}),
					toast,
				);
				if (result.warnings.length) {
					toast({
						status: "warning",
						title: t("settings.backup.importWarnings"),
						description: result.warnings.join("\n"),
						duration: 8000,
						isClosable: true,
					});
				}
				setSelectedFile(null);
				setDialog(null);
			},
			onError: (error) => {
				generateErrorMessage(error, toast);
			},
			onSettled: () => setUploadProgress(null),
		},
	);

	const vpnUIImportMutation = useMutation(
		(file: File) =>
			importVPNUIBackup(
				file,
				Number(vpnUIServiceID),
				duplicatePolicy,
				setUploadProgress,
			),
		{
			onMutate: () => setUploadProgress(0),
			onSuccess: (result) => {
				generateSuccessMessage(
					t("settings.backup.vpnUIImportDone", {
						imported: result.imported,
						skipped: result.skipped,
					}),
					toast,
				);
				if (result.warnings.length) {
					toast({
						status: "warning",
						title: t("settings.backup.importWarnings"),
						description: result.warnings.join("\n"),
						duration: 10000,
						isClosable: true,
					});
				}
				setSelectedFile(null);
				setDialog(null);
			},
			onError: (error) => {
				generateErrorMessage(error, toast);
			},
			onSettled: () => setUploadProgress(null),
		},
	);

	const openDialog = (nextDialog: Exclude<BackupDialog, null>) => {
		setMenuOpen(false);
		setDialog(nextDialog);
	};

	const handleImport = () => {
		if (!selectedFile) {
			toast({ status: "warning", title: t("settings.backup.fileRequired") });
			return;
		}
		importMutation.mutate(selectedFile);
	};

	const handleVPNUIImport = () => {
		if (!selectedFile) {
			toast({ status: "warning", title: t("settings.backup.vpnUIFileRequired") });
			return;
		}
		if (!vpnUIServiceID) {
			toast({ status: "warning", title: t("settings.backup.vpnUIServiceRequired") });
			return;
		}
		vpnUIImportMutation.mutate(selectedFile);
	};

	return (
		<>
			<Popover
				isOpen={isMenuOpen}
				onOpen={() => setMenuOpen(true)}
				onClose={() => setMenuOpen(false)}
				placement="bottom-end"
			>
				<PopoverTrigger>
					<Button
						size="sm"
						variant="outline"
						borderRadius="full"
						leftIcon={<ArchiveBoxIcon width={16} height={16} />}
						isDisabled={runtimeLoading}
						isLoading={runtimeLoading}
						w="full"
					>
						{t("settings.backup.tabTitle")}
					</Button>
				</PopoverTrigger>
				<PopoverContent
					w="min(320px, calc(100vw - 24px))"
					borderRadius="xl"
					boxShadow="2xl"
				>
					<PopoverHeader fontWeight="bold" py={3}>
						{t("settings.backup.title")}
					</PopoverHeader>
					<PopoverBody p={3}>
						<Stack spacing={2}>
							<Button
								variant="ghost"
								justifyContent="flex-start"
								leftIcon={<ArrowUpTrayIcon width={18} height={18} />}
								onClick={() => openDialog("import")}
								isDisabled={!backupActionsAvailable}
							>
								{t("settings.backup.import")}
							</Button>
							<Button
								variant="ghost"
								justifyContent="flex-start"
								leftIcon={<ArrowDownTrayIcon width={18} height={18} />}
								onClick={() => openDialog("export")}
								isDisabled={!backupActionsAvailable}
							>
								{t("settings.backup.exportTitle")}
							</Button>
							<Button
								variant="ghost"
								justifyContent="flex-start"
								leftIcon={<ArrowUpTrayIcon width={18} height={18} />}
								onClick={() => openDialog("vpn-ui")}
							>
								{t("settings.backup.vpnUIImport")}
							</Button>
						</Stack>
					</PopoverBody>
				</PopoverContent>
			</Popover>

			<Modal
				isOpen={dialog === "import"}
				onClose={() => setDialog(null)}
				isCentered
				size="xl"
				closeOnOverlayClick={!importMutation.isLoading}
			>
				<ModalOverlay bg="blackAlpha.500" />
				<ModalContent
					borderWidth="1px"
					borderColor={borderColor}
					borderRadius="2xl"
					boxShadow="2xl"
					mx={{ base: 4, sm: 0 }}
				>
					<ModalHeader>{t("settings.backup.import")}</ModalHeader>
					<ModalCloseButton isDisabled={importMutation.isLoading} />
					<ModalBody>
						<Stack spacing={4}>
							<Text fontSize="sm" color="panel.textMuted">
								{t("settings.backup.importHint")}
							</Text>
							<Alert status="warning" borderRadius="lg">
								<AlertIcon />
								<Text fontSize="sm">
									{t("settings.backup.autoDetectImportWarning")}
								</Text>
							</Alert>
							<FormControl isRequired>
								<FormLabel>{t("settings.backup.file")}</FormLabel>
								<FileDropzone
									accept=".rbbackup,application/vnd.antimage.backup,application/gzip"
									isDisabled={
										!backupActionsAvailable || importMutation.isLoading
									}
									selectedFile={selectedFile}
									title={t("settings.backup.dropTitle")}
									description={t("settings.backup.dropHint")}
									emptyText={t("settings.backup.selectFile")}
									onFileSelect={setSelectedFile}
								/>
							</FormControl>
							{importMutation.isLoading && uploadProgress !== null && (
								<Stack spacing={2} aria-live="polite">
									<Text fontSize="sm" fontWeight="semibold">
										{uploadProgress < 100
											? t("settings.backup.uploadProgress", {
													percent: uploadProgress,
												})
											: t("settings.backup.processing")}
									</Text>
									<Progress
										value={uploadProgress}
										isIndeterminate={uploadProgress >= 100}
										colorScheme="pink"
										borderRadius="full"
										size="sm"
									/>
								</Stack>
							)}
						</Stack>
					</ModalBody>
					<ModalFooter gap={2} borderTopWidth="1px" borderColor={borderColor}>
						<Button
							variant="ghost"
							onClick={() => setDialog(null)}
							isDisabled={importMutation.isLoading}
						>
							{t("cancel")}
						</Button>
						<Button
							colorScheme="red"
							leftIcon={<ArrowUpTrayIcon width={16} height={16} />}
							onClick={handleImport}
							isLoading={importMutation.isLoading}
						>
							{t("settings.backup.import")}
						</Button>
					</ModalFooter>
				</ModalContent>
			</Modal>

			<Modal
				isOpen={dialog === "vpn-ui"}
				onClose={() => setDialog(null)}
				isCentered
				size="xl"
				closeOnOverlayClick={!vpnUIImportMutation.isLoading}
			>
				<ModalOverlay bg="blackAlpha.500" />
				<ModalContent borderWidth="1px" borderColor={borderColor} borderRadius="2xl" boxShadow="2xl" mx={{ base: 4, sm: 0 }}>
					<ModalHeader>{t("settings.backup.vpnUIImport")}</ModalHeader>
					<ModalCloseButton isDisabled={vpnUIImportMutation.isLoading} />
					<ModalBody>
						<Stack spacing={4}>
							<Text fontSize="sm" color="panel.textMuted">{t("settings.backup.vpnUIHint")}</Text>
							<Alert status="warning" borderRadius="lg"><AlertIcon /><Text fontSize="sm">{t("settings.backup.vpnUIWarning")}</Text></Alert>
							<FormControl isRequired>
								<FormLabel>{t("settings.backup.vpnUIFile")}</FormLabel>
								<FileDropzone accept=".db,.sqlite,.sqlite3,application/x-sqlite3" isDisabled={vpnUIImportMutation.isLoading} selectedFile={selectedFile} title={t("settings.backup.vpnUIDropTitle")} description={t("settings.backup.vpnUIDropHint")} emptyText={t("settings.backup.selectFile")} onFileSelect={setSelectedFile} />
							</FormControl>
							<FormControl isRequired>
								<FormLabel>{t("settings.backup.vpnUIService")}</FormLabel>
								<Select value={vpnUIServiceID} showSearch onChange={(event) => setVPNUIServiceID(event.target.value)}>
									<option value="">{t("settings.backup.vpnUIServicePlaceholder")}</option>
									{serviceOptions.filter((service) => service.has_hosts).map((service) => <option key={service.id} value={service.id}>{service.name}</option>)}
								</Select>
							</FormControl>
							<FormControl>
								<FormLabel>{t("settings.backup.vpnUIDuplicates")}</FormLabel>
								<Select value={duplicatePolicy} showSearch={false} onChange={(event) => setDuplicatePolicy(event.target.value as "skip" | "rename")}>
									<option value="skip">{t("settings.backup.vpnUIDuplicateSkip")}</option>
									<option value="rename">{t("settings.backup.vpnUIDuplicateRename")}</option>
								</Select>
							</FormControl>
							{vpnUIImportMutation.isLoading && uploadProgress !== null && <Progress value={uploadProgress} isIndeterminate={uploadProgress >= 100} colorScheme="pink" borderRadius="full" size="sm" />}
						</Stack>
					</ModalBody>
					<ModalFooter gap={2} borderTopWidth="1px" borderColor={borderColor}>
						<Button variant="ghost" onClick={() => setDialog(null)} isDisabled={vpnUIImportMutation.isLoading}>{t("cancel")}</Button>
						<Button colorScheme="primary" leftIcon={<ArrowUpTrayIcon width={16} height={16} />} onClick={handleVPNUIImport} isLoading={vpnUIImportMutation.isLoading}>{t("settings.backup.vpnUIImport")}</Button>
					</ModalFooter>
				</ModalContent>
			</Modal>

			<Modal
				isOpen={dialog === "export"}
				onClose={() => setDialog(null)}
				isCentered
				size="md"
				closeOnOverlayClick={!exportMutation.isLoading}
			>
				<ModalOverlay bg="blackAlpha.500" />
				<ModalContent
					borderWidth="1px"
					borderColor={borderColor}
					borderRadius="2xl"
					boxShadow="2xl"
					mx={{ base: 4, sm: 0 }}
				>
					<ModalHeader>{t("settings.backup.exportTitle")}</ModalHeader>
					<ModalCloseButton isDisabled={exportMutation.isLoading} />
					<ModalBody>
						<Stack spacing={4}>
							<Text fontSize="sm" color="panel.textMuted">
								{t("settings.backup.exportHint")}
							</Text>
							<FormControl>
								<FormLabel>{t("settings.telegram.backupScope")}</FormLabel>
								<Select
									value={exportScope}
									showSearch={false}
									onChange={(event) =>
										setExportScope(event.target.value as AntiMageBackupScope)
									}
								>
									<option value="database">
										{t("settings.backup.databaseOnly")}
									</option>
									<option value="full">{t("settings.backup.full")}</option>
								</Select>
							</FormControl>
						</Stack>
					</ModalBody>
					<ModalFooter gap={2} borderTopWidth="1px" borderColor={borderColor}>
						<Button
							variant="ghost"
							onClick={() => setDialog(null)}
							isDisabled={exportMutation.isLoading}
						>
							{t("cancel")}
						</Button>
						<Button
							colorScheme="primary"
							leftIcon={<ArrowDownTrayIcon width={16} height={16} />}
							onClick={() => exportMutation.mutate(exportScope)}
							isLoading={exportMutation.isLoading}
						>
							{t("settings.backup.download")}
						</Button>
					</ModalFooter>
				</ModalContent>
			</Modal>
		</>
	);
};
